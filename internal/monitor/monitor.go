// Package monitor schedules the configured checks and stores their results.
package monitor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

// DefaultHeartbeatInterval is how often the daemon records that it is alive.
const DefaultHeartbeatInterval = 10 * time.Second

// DefaultDiscoveryInterval bounds how long a newly added check waits to start.
const DefaultDiscoveryInterval = 2 * time.Second

// writeTimeout bounds each database write, so a locked database cannot
// stall a check loop forever.
const writeTimeout = 10 * time.Second

// Runner runs one check.
type Runner interface {
	Run(ctx context.Context, c config.Check) check.Result
}

// Storage is the append-only definition and observation API used by Run.
// *store.Store implements it.
type Storage interface {
	Checks(context.Context) ([]config.Check, error)
	ImportChecks(context.Context, []config.Check) error
	Record(context.Context, check.Result) error
	Beat(context.Context, time.Time) error
}

// Options configures Run.
type Options struct {
	// Checks is an optional additive import, not the scheduling source.
	Checks            []config.Check
	Store             Storage
	Runner            Runner
	HeartbeatInterval time.Duration
	// DiscoveryInterval defaults to DefaultDiscoveryInterval when nonpositive.
	DiscoveryInterval time.Duration
	Logger            *slog.Logger
}

// Run imports optional Checks, reads SQLite definitions, then starts each check
// immediately and every Interval. Discovery starts only new names; existing
// loops are never restarted. Transient discovery errors are logged and retried.
// Run joins every loop on cancellation; nothing is written after it returns.
func Run(ctx context.Context, opts Options) error {
	if len(opts.Checks) > 0 {
		if err := opts.Store.ImportChecks(ctx, opts.Checks); err != nil {
			return err
		}
	}
	checks, err := opts.Store.Checks(ctx)
	if err != nil {
		return err
	}
	if opts.DiscoveryInterval <= 0 {
		opts.DiscoveryInterval = DefaultDiscoveryInterval
	}
	opts.Logger.Info("monitoring started", "checks", len(checks))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		heartbeat(ctx, opts)
	}()
	started := make(map[string]bool)
	start := func(checks []config.Check) {
		for _, c := range checks {
			if started[c.Name] || ctx.Err() != nil {
				continue
			}
			started[c.Name] = true
			wg.Add(1)
			go func() {
				defer wg.Done()
				loop(ctx, opts, c)
			}()
		}
	}
	start(checks)
	ticker := time.NewTicker(opts.DiscoveryInterval)
	defer ticker.Stop()
discovery:
	for {
		select {
		case <-ctx.Done():
			break discovery
		case <-ticker.C:
			checks, err := opts.Store.Checks(ctx)
			if err != nil {
				if ctx.Err() == nil {
					opts.Logger.Error("discovering checks failed", "error", err)
				}
				continue
			}
			start(checks)
		}
	}
	wg.Wait()
	opts.Logger.Info("monitoring stopped")
	return nil
}

func heartbeat(ctx context.Context, opts Options) {
	ticker := time.NewTicker(opts.HeartbeatInterval)
	defer ticker.Stop()
	for {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		if err := opts.Store.Beat(wctx, time.Now()); err != nil {
			opts.Logger.Error("heartbeat failed", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func loop(ctx context.Context, opts Options, c config.Check) {
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	var last check.Status
	for {
		res := opts.Runner.Run(ctx, c)
		if res.Status == check.StatusCanceled || ctx.Err() != nil {
			return
		}
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		if err := opts.Store.Record(wctx, res); err != nil {
			opts.Logger.Error("storing result failed", "check", c.Name, "error", err)
		}
		cancel()
		if res.Status != last {
			logTransition(opts.Logger, c, res)
			last = res.Status
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func logTransition(logger *slog.Logger, c config.Check, res check.Result) {
	if res.Status == check.StatusUp {
		logger.Info("check up", "check", c.Name, "url", c.URL, "latency", res.Latency)
		return
	}
	logger.Warn("check down", "check", c.Name, "url", c.URL, "error", res.Error)
}
