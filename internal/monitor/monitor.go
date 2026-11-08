// Package monitor schedules the configured checks and stores their results.
package monitor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
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

// MuteStore is the optional API for holding and reconciling alerts.
type MuteStore interface {
	Mute(context.Context, string) (config.Mute, bool, error)
	ClearMute(context.Context, string, time.Time) error
}

// CertStore is the optional certificate observation persistence API.
type CertStore interface {
	Cert(context.Context, string) (config.Cert, bool, error)
	SaveCert(context.Context, config.Cert) error
	DeleteCert(context.Context, string) error
}

// CertAlerter receives certificate warnings without blocking.
type CertAlerter interface {
	NotifyCert(config.Check, config.Cert, time.Duration)
}

// Alerter receives transitions; Notify must not block.
type Alerter interface {
	Notify(config.Check, check.Result)
}

// LatestReader provides the most recent recorded status.
type LatestReader interface {
	Latest(context.Context, string) (check.Result, bool, error)
}

// RecentReader is the optional API that replays stored results, newest first,
// to rebuild the failure streak after a restart. *store.Store implements it.
type RecentReader interface {
	Recent(context.Context, string, int) ([]check.Result, error)
}

// replayLimit bounds how much history rebuilds a streak: enough for the
// largest thresholds with room to spare.
const replayLimit = 100

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
	// Now is the clock for mutes and maintenance windows; nil is time.Now.
	Now func() time.Time
	// Pings is whether the daemon can send pings (see check.DetectPings).
	// With config.PingDenied ping checks are not run: they record nothing
	// and send no alert, so they stay pending. Other values run them.
	Pings config.PingPermission
}

// Run reads the SQLite definitions, then starts each check
// immediately and every Interval. Discovery cancels removed loops and restarts
// edited definitions, preserving their last transition status. Unchanged loops
// are left alone. Transient discovery errors are logged and retried.
// Run joins every loop on cancellation; nothing is written after it returns.
func Run(ctx context.Context, opts Options) error {
	checks, err := opts.Store.Checks(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// Shutdown before the first read is a clean stop.
			return nil
		}
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
				if c, exists := definitions[name]; exists && c.SameDefinition(active.definition) {
					previous[name] = active.last
				}
				delete(running, name)
			default:
			}
		}
		// Cancel all obsolete loops before joining any one of them.
		// Joining means a Runner that ignores cancellation can stall discovery.
		for name, active := range running {
			if c, exists := definitions[name]; !exists || !c.SameDefinition(active.definition) {
				active.cancel()
			}
		}
		for name, active := range running {
			if c, exists := definitions[name]; !exists || !c.SameDefinition(active.definition) {
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
	if opts.Pings.Holds(c) {
		// Without permission a ping says nothing about the host, and an
		// alert owed from before would report a state nobody can check.
		<-ctx.Done()
		return last
	}
	var streak check.Streak
	var notified bool
	muteStore, hasMutes := opts.Store.(MuteStore)
	readMute := func() (config.Mute, bool) {
		if !hasMutes {
			return config.Mute{}, false
		}
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		defer cancel()
		m, found, err := muteStore.Mute(readCtx, c.Name)
		if err != nil {
			opts.Logger.Error("reading mute failed", "check", c.Name, "error", err)
			return config.Mute{}, false
		}
		return m, found
	}
	// heldEvent is the alert already logged as held, so the repeated settling
	// of one owed alert logs it once; certHeld is the same for the certificate
	// warning threshold. Both start from the delivery log, so a restart or an
	// edit does not log the same hold again.
	var heldEvent store.Event
	var certHeld int
	if opts.Alerter != nil {
		streak = initialStreak(ctx, opts, c)
		m, found := readMute()
		notified = loadNotified(ctx, opts, c, streak, m, found)
		heldEvent = loggedHold(ctx, opts, c, streak.Alerting, notified)
		certHeld = loggedCertHold(ctx, opts, c)
	}
	// suppressed is a manual mute or an active maintenance window.
	suppressed := func() suppression {
		m, found := readMute()
		now := opts.now()
		active := found && m.Until.After(now)
		return suppression{mute: m, found: found, muted: active, active: active || config.EvaluateMaintenance(windows(ctx, opts, c.Name), now).Active}
	}
	// settle sends what the recipients do not know once nothing holds alerts
	// back, and forgets a mute that has ended with no window still running.
	settle := func(res check.Result, s suppression) {
		if c.Channel == "" {
			// Nothing can be handed over, so recipients learn nothing. A
			// recovery nobody could be told about owes no later alert.
			if notified && !streak.Alerting {
				notified = false
				saveNotified(ctx, opts, c, notified)
			}
		} else if !s.active && streak.Alerting != notified {
			notified = streak.Alerting
			heldEvent = ""
			opts.Alerter.Notify(c, res)
			saveNotified(ctx, opts, c, notified)
		} else if s.active && streak.Alerting != notified {
			event := store.EventRecovered
			if streak.Alerting {
				event = store.EventDown
			}
			if event != heldEvent {
				heldEvent = event
				recordHeld(ctx, opts, c, event, s)
			}
		} else {
			heldEvent = ""
		}
		if s.found && !s.active {
			clearCtx, clearCancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
			if err := muteStore.ClearMute(clearCtx, c.Name, s.mute.Until); err != nil {
				opts.Logger.Error("clearing mute failed", "check", c.Name, "error", err)
			}
			clearCancel()
		}
	}
	// Suppression can end between results (a window ends or is deleted), so
	// it is rechecked every discovery interval against the last result.
	var recheck <-chan time.Time
	var latest check.Result
	if opts.Alerter != nil {
		// What is owed from before this loop started (a channel just attached
		// to a monitor that is down) goes out now, not after the first check.
		latest = lastResult(ctx, opts, c)
		t := time.NewTicker(opts.DiscoveryInterval)
		defer t.Stop()
		recheck = t.C
	}
	if latest.CheckName != "" {
		settle(latest, suppressed())
	}
	// observe applies one stored result: the alert streak, the certificate
	// and the transition log.
	observe := func(res check.Result) {
		if opts.Alerter != nil {
			streak.Observe(res.Status, c.AlertAfter, c.RecoverAfter)
			sup := suppressed()
			if res.Cert != nil {
				observeCert(ctx, opts, c, res, sup, &certHeld)
			}
			latest = res
			settle(res, sup)
		}
		if res.Status != last {
			logTransition(opts.Logger, c, res)
			last = res.Status
		}
	}
	if c.Kind == config.CheckHeartbeat {
		watchHeartbeat(ctx, opts, c, observe, func() {
			if opts.Alerter != nil && latest.CheckName != "" {
				settle(latest, suppressed())
			}
		})
		return last
	}
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
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
		observe(res)
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				return last
			case <-ticker.C:
				waiting = false
			case <-recheck:
				if latest.CheckName != "" {
					settle(latest, suppressed())
				}
			}
		}
	}
}

// observeCert saves the certificate and warns when a threshold is due. A
// warning held by suppression is logged once per threshold (certHeld).
func observeCert(ctx context.Context, opts Options, c config.Check, res check.Result, sup suppression, certHeld *int) {
	certs, ok := opts.Store.(CertStore)
	if !ok {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if c.TLSOff {
		if err := certs.DeleteCert(wctx, c.Name); err != nil {
			opts.Logger.Error("forgetting certificate failed", "check", c.Name, "error", err)
		}
		return
	}
	old, found, err := certs.Cert(wctx, c.Name)
	if err != nil {
		opts.Logger.Error("reading certificate failed", "check", c.Name, "error", err)
		return
	}
	cert := config.Cert{Check: c.Name, Host: res.Cert.Host, Issuer: res.Cert.Issuer, Serial: res.Cert.Serial, NotAfter: res.Cert.NotAfter, Observed: res.At}
	if found && old.Serial == cert.Serial && old.NotAfter.Equal(cert.NotAfter) {
		cert.Notified = old.Notified
	}
	remaining := cert.NotAfter.Sub(res.At)
	due := dueThreshold(remaining, cert.Notified)
	if alerter, ok := opts.Alerter.(CertAlerter); ok && due != 0 && c.Channel != "" {
		if !sup.active {
			cert.Notified = due
			*certHeld = 0
			alerter.NotifyCert(c, cert, remaining)
		} else if *certHeld != due {
			*certHeld = due
			recordHeld(ctx, opts, c, store.EventCert, sup)
		}
	}
	if err := certs.SaveCert(wctx, cert); err != nil {
		opts.Logger.Error("saving certificate failed", "check", c.Name, "error", err)
	}
}

// dueThreshold is the warning threshold (days) due with remaining time left
// when the last one sent was notified (0 for none), or 0 when none is due.
func dueThreshold(remaining time.Duration, notified int) int {
	due := 0
	for _, threshold := range config.CertWarnDays {
		if remaining <= time.Duration(threshold)*24*time.Hour && (notified == 0 || threshold < notified) && (due == 0 || threshold < due) {
			due = threshold
		}
	}
	return due
}

// initialStreak rebuilds the alert state from stored results so a restart
// neither resends an alert nor forgets a failure streak in progress.
func initialStreak(ctx context.Context, opts Options, c config.Check) check.Streak {
	readCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if reader, ok := opts.Store.(RecentReader); ok {
		results, err := reader.Recent(readCtx, c.Name, replayLimit)
		if err != nil {
			opts.Logger.Error("reading recent results failed", "check", c.Name, "error", err)
			return check.Streak{}
		}
		return check.Replay(results, c.AlertAfter, c.RecoverAfter)
	}
	if reader, ok := opts.Store.(LatestReader); ok {
		result, found, err := reader.Latest(readCtx, c.Name)
		if err != nil {
			opts.Logger.Error("reading last status failed", "check", c.Name, "error", err)
		} else if found && result.Status == check.StatusDown {
			return check.Streak{Alerting: true, Failures: 1}
		}
	}
	return check.Streak{}
}

// lastResult is the newest stored result, or the zero value when none is
// known, so a loop can reconcile before its first check finishes.
func lastResult(ctx context.Context, opts Options, c config.Check) check.Result {
	reader, ok := opts.Store.(LatestReader)
	if !ok {
		return check.Result{}
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	res, found, err := reader.Latest(readCtx, c.Name)
	if err != nil || !found {
		return check.Result{}
	}
	return res
}

func logTransition(logger *slog.Logger, c config.Check, res check.Result) {
	if c.Kind == config.CheckHeartbeat {
		if res.Status == check.StatusUp {
			logger.Info("check up", "check", c.Name)
		} else {
			logger.Warn("check down", "check", c.Name, "error", res.Error)
		}
		return
	}
	if res.Status == check.StatusUp {
		logger.Info("check up", "check", c.Name, "url", c.Target(), "latency", res.Latency)
		return
	}
	logger.Warn("check down", "check", c.Name, "url", c.Target(), "error", res.Error)
}
