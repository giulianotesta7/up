// Package monitor schedules the configured checks and stores their results.
package monitor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// DefaultHeartbeatInterval is how often the daemon records that it is alive.
const DefaultHeartbeatInterval = 10 * time.Second

// writeTimeout bounds each database write, so a locked database cannot
// stall a check loop forever.
const writeTimeout = 10 * time.Second

// Runner runs one check.
type Runner interface {
	Run(ctx context.Context, c config.Check) check.Result
}

// Options configures Run.
type Options struct {
	Checks            []config.Check
	Store             *store.Store
	Runner            Runner
	HeartbeatInterval time.Duration
	Logger            *slog.Logger
}

// Run stores the checks, then runs each one right away and every Interval
// after that, recording the results, until ctx is done. It also records a
// heartbeat every HeartbeatInterval. It returns once every loop stopped;
// nothing is written after it returns.
func Run(ctx context.Context, opts Options) error {
	if err := opts.Store.SetChecks(ctx, opts.Checks); err != nil {
		return err
	}
	opts.Logger.Info("monitoring started", "checks", len(opts.Checks))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		heartbeat(ctx, opts)
	}()
	for _, c := range opts.Checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loop(ctx, opts, c)
		}()
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
