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

// DefaultDiscoveryInterval bounds how long definition changes wait to be discovered.
const DefaultDiscoveryInterval = 2 * time.Second

// DefaultPruneInterval is how often results older than the retention are removed.
const DefaultPruneInterval = time.Hour

// writeTimeout bounds each database write, so a locked database cannot
// stall a check loop forever.
const writeTimeout = 10 * time.Second

// Runner runs one check.
type Runner interface {
	Run(ctx context.Context, c config.Check) check.Result
}

// Storage is the definition and observation API used by Run.
// *store.Store implements it.
type Storage interface {
	Checks(context.Context) ([]config.Check, error)
	Record(context.Context, check.Result) error
	Beat(context.Context, time.Time) error
}

// Pruner is the optional storage API that removes results older than a
// cutoff. *store.Store implements it.
type Pruner interface {
	Prune(context.Context, time.Time) (int64, error)
}

// DefinedRecorder is the optional definition-aware result API. Run prefers it
// over Record so deleted or edited definitions reject obsolete results. Storage
// remains compatible with record-only implementations and test fakes.
type DefinedRecorder interface {
	RecordDefined(context.Context, config.Check, check.Result) (bool, error)
}

// Alerter receives transitions; Notify must not block.
type Alerter interface {
	Notify(config.Check, check.Result)
}

// LatestReader provides the most recent recorded status.
type LatestReader interface {
	Latest(context.Context, string) (check.Result, bool, error)
}

// Options configures Run.
type Options struct {
	Alerter           Alerter
	Store             Storage
	Runner            Runner
	HeartbeatInterval time.Duration
	// DiscoveryInterval defaults to DefaultDiscoveryInterval when nonpositive.
	DiscoveryInterval time.Duration
	// Retention is how long results are kept; zero keeps them forever. It
	// only applies when Store also implements Pruner.
	Retention time.Duration
	// PruneInterval defaults to DefaultPruneInterval when nonpositive.
	PruneInterval time.Duration
	Logger        *slog.Logger
}

// Run reads the SQLite definitions, then starts each check
// immediately and every Interval. Discovery cancels removed loops and restarts
// edited definitions, preserving their last transition status. Unchanged loops
// are left alone. Transient discovery errors are logged and retried.
// Run joins every loop on cancellation; nothing is written after it returns.
func Run(ctx context.Context, opts Options) error {
	checks, err := opts.Store.Checks(ctx)
	if err != nil {
		return err
	}
	if opts.DiscoveryInterval <= 0 {
		opts.DiscoveryInterval = DefaultDiscoveryInterval
	}
	if opts.PruneInterval <= 0 {
		opts.PruneInterval = DefaultPruneInterval
	}
	opts.Logger.Info("monitoring started", "checks", len(checks))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		heartbeat(ctx, opts)
	}()
	if pruner, ok := opts.Store.(Pruner); ok && opts.Retention > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prune(ctx, opts, pruner)
		}()
	}
	type runningLoop struct {
		definition config.Check
		cancel     context.CancelFunc
		done       chan struct{}
		// Only read after done closes, which synchronizes the loop's final write.
		last check.Status
	}
	running := make(map[string]*runningLoop)
	reconcile := func(checks []config.Check) {
		definitions := make(map[string]config.Check, len(checks))
		for _, c := range checks {
			definitions[c.Name] = c
		}
		previous := make(map[string]check.Status)
		// A loop can finish before discovery (for example, a rejected result).
		// Do not mistake its retained entry for a live, unchanged loop.
		for name, active := range running {
			select {
			case <-active.done:
				if c, exists := definitions[name]; exists && c == active.definition {
					previous[name] = active.last
				}
				delete(running, name)
			default:
			}
		}
		// Cancel all obsolete loops before joining any one of them.
		// Joining means a Runner that ignores cancellation can stall discovery.
		for name, active := range running {
			if c, exists := definitions[name]; !exists || c != active.definition {
				active.cancel()
			}
		}
		for name, active := range running {
			if c, exists := definitions[name]; !exists || c != active.definition {
				<-active.done
				if exists {
					previous[name] = active.last
				}
				delete(running, name)
			}
		}
		for _, c := range checks {
			if _, exists := running[c.Name]; exists || ctx.Err() != nil {
				continue
			}
			loopCtx, cancel := context.WithCancel(ctx)
			active := &runningLoop{definition: c, cancel: cancel, done: make(chan struct{}), last: previous[c.Name]}
			running[c.Name] = active
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer close(active.done)
				defer cancel()
				active.last = loop(loopCtx, opts, c, active.last)
			}()
		}
	}
	reconcile(checks)
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
			reconcile(checks)
		}
	}
	wg.Wait()
	opts.Logger.Info("monitoring stopped")
	return nil
}

func prune(ctx context.Context, opts Options, pruner Pruner) {
	ticker := time.NewTicker(opts.PruneInterval)
	defer ticker.Stop()
	for {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		n, err := pruner.Prune(wctx, time.Now().Add(-opts.Retention))
		cancel()
		switch {
		case err != nil:
			opts.Logger.Error("pruning results failed", "error", err)
		case n > 0:
			opts.Logger.Info("pruned old results", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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

func loop(ctx context.Context, opts Options, c config.Check, last check.Status) check.Status {
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	var baseline check.Status
	if reader, ok := opts.Store.(LatestReader); opts.Alerter != nil && ok {
		readCtx, cancel := context.WithTimeout(ctx, writeTimeout)
		result, found, err := reader.Latest(readCtx, c.Name)
		cancel()
		if err != nil {
			opts.Logger.Error("reading last status failed", "check", c.Name, "error", err)
		} else if found {
			baseline = result.Status
		}
	}
	for {
		res := opts.Runner.Run(ctx, c)
		if res.Status == check.StatusCanceled || ctx.Err() != nil {
			return last
		}
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		stored := true
		var err error
		if recorder, ok := opts.Store.(DefinedRecorder); ok {
			stored, err = recorder.RecordDefined(wctx, c, res)
		} else {
			err = opts.Store.Record(wctx, res)
		}
		cancel()
		if err != nil {
			opts.Logger.Error("storing result failed", "check", c.Name, "error", err)
		} else if !stored {
			return last
		}
		if opts.Alerter != nil {
			if c.Channel != "" && ((baseline == "" && res.Status == check.StatusDown) || (baseline != "" && res.Status != baseline)) {
				opts.Alerter.Notify(c, res)
			}
			baseline = res.Status
		}
		if res.Status != last {
			logTransition(opts.Logger, c, res)
			last = res.Status
		}
		select {
		case <-ctx.Done():
			return last
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
