package monitor

import (
	"context"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
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
