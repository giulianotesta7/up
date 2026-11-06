package monitor

import (
	"context"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// WindowStore is the optional API for a check's recurring maintenance windows.
// *store.Store implements it. While a window is active, alerts and
// certificate warnings are held exactly like a manual mute.
type WindowStore interface {
	MaintenanceWindows(context.Context, string) ([]config.MaintenanceWindow, error)
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// suppression is why a check's alerts are held at one instant.
type suppression struct {
	mute  config.Mute
	found bool
	// active is a manual mute or a maintenance window in force.
	active bool
	// muted is a manual mute in force; the rest of active is a window.
	muted bool
}

// reason is why alerts are held, as the delivery log shows it.
func (s suppression) reason() string {
	if s.muted {
		return "muted"
	}
	return "maintenance window"
}

// AttemptStore is the optional API that logs alerts held back by a mute or a
// maintenance window. *store.Store implements it. Sent and failed deliveries
// are logged by the notifier.
type AttemptStore interface {
	RecordAttempt(context.Context, store.Attempt) error
}

// recordHeld logs that the event is being held for the check's channel. A
// failure to log is reported and never changes the alert.
func recordHeld(ctx context.Context, opts Options, c config.Check, event store.Event, s suppression) {
	attempts, ok := opts.Store.(AttemptStore)
	if !ok {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	err := attempts.RecordAttempt(wctx, store.Attempt{At: opts.now(), Monitor: c.Name, Channel: c.Channel, Event: event, Outcome: store.OutcomeHeld, Reason: s.reason()})
	if err != nil {
		opts.Logger.Error("recording held alert failed", "check", c.Name, "error", err)
	}
}

// AttemptReader is the optional API that reads the delivery log back, so a
// loop that starts again knows which owed alert it already logged as held.
// *store.Store implements it.
type AttemptReader interface {
	LatestAttempt(context.Context, string, string, ...store.Event) (store.Attempt, bool, error)
}

// latestHeld is the newest logged attempt of the check's channel for events
// when it is a hold. A read error is logged and counts as none.
func latestHeld(ctx context.Context, opts Options, c config.Check, events ...store.Event) (store.Attempt, bool) {
	reader, ok := opts.Store.(AttemptReader)
	if !ok || c.Channel == "" {
		return store.Attempt{}, false
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	a, found, err := reader.LatestAttempt(readCtx, c.Name, c.Channel, events...)
	if err != nil {
		opts.Logger.Error("reading delivery log failed", "check", c.Name, "error", err)
		return store.Attempt{}, false
	}
	return a, found && a.Outcome == store.OutcomeHeld
}

// loggedHold is the alert owed when a loop starts that the delivery log
// already shows as held, or "" when none is. Only the alert still owed
// counts: a hold that ended without a delivery (the monitor recovered under
// it) left history that clears the owed alert, so a later outage is a new one.
func loggedHold(ctx context.Context, opts Options, c config.Check, alerting, notified bool) store.Event {
	if alerting == notified {
		return ""
	}
	owed := store.EventRecovered
	if alerting {
		owed = store.EventDown
	}
	if a, held := latestHeld(ctx, opts, c, store.EventDown, store.EventRecovered); held && a.Event == owed {
		return owed
	}
	return ""
}

// loggedCertHold is the certificate threshold the delivery log already shows
// as held, or 0: the threshold that was due when the newest certificate
// attempt was held, against the stored certificate.
func loggedCertHold(ctx context.Context, opts Options, c config.Check) int {
	certs, ok := opts.Store.(CertStore)
	if !ok {
		return 0
	}
	a, held := latestHeld(ctx, opts, c, store.EventCert)
	if !held {
		return 0
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	cert, found, err := certs.Cert(readCtx, c.Name)
	if err != nil {
		opts.Logger.Error("reading certificate failed", "check", c.Name, "error", err)
		return 0
	}
	if !found {
		return 0
	}
	return dueThreshold(cert.NotAfter.Sub(a.At), cert.Notified)
}

// windows reads the check's windows fresh, so edits and deletions apply on
// the next evaluation. A read error is logged and counts as no windows.
func windows(ctx context.Context, opts Options, name string) []config.MaintenanceWindow {
	reader, ok := opts.Store.(WindowStore)
	if !ok {
		return nil
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	ws, err := reader.MaintenanceWindows(readCtx, name)
	if err != nil {
		opts.Logger.Error("reading maintenance windows failed", "check", name, "error", err)
		return nil
	}
	return ws
}

// AlertStateStore is the optional API that persists what the recipients were
// last told (whether the check is down), so a restart, a definition edit or a
// window that held results back never has to guess it from history.
// *store.Store implements it.
type AlertStateStore interface {
	AlertState(context.Context, string) (known, found bool, err error)
	SetAlertState(context.Context, string, bool) error
}

// loadNotified is what the recipients know when a loop starts: the live mute's
// snapshot of it, else the persisted state, else the streak rebuilt from
// history (databases from before the state was kept), which only counts when
// a channel exists: without one nothing was ever handed over. A rebuilt value
// is persisted at once, so later restarts do not depend on it.
func loadNotified(ctx context.Context, opts Options, c config.Check, streak check.Streak, mute config.Mute, hasMute bool) bool {
	if hasMute {
		return mute.WasAlerting
	}
	fallback := streak.Alerting && c.Channel != ""
	states, ok := opts.Store.(AlertStateStore)
	if !ok {
		return fallback
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	known, found, err := states.AlertState(readCtx, c.Name)
	if err != nil {
		opts.Logger.Error("reading alert state failed", "check", c.Name, "error", err)
		return fallback
	}
	if !found {
		saveNotified(ctx, opts, c, fallback)
		return fallback
	}
	return known
}

// saveNotified persists what the recipients now know.
func saveNotified(ctx context.Context, opts Options, c config.Check, known bool) {
	states, ok := opts.Store.(AlertStateStore)
	if !ok {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if err := states.SetAlertState(wctx, c.Name, known); err != nil {
		opts.Logger.Error("saving alert state failed", "check", c.Name, "error", err)
	}
}
