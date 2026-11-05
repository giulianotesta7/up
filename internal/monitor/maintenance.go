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

// initialNotified is what the recipients know after a restart in the middle
// of a window: the state before the contiguous run of results the window has
// held back, because those were never reported.
func initialNotified(ctx context.Context, opts Options, c config.Check, streak check.Streak) bool {
	reader, ok := opts.Store.(RecentReader)
	if !ok {
		return streak.Alerting
	}
	ws := windows(ctx, opts, c.Name)
	if !config.EvaluateMaintenance(ws, opts.now()).Active {
		return streak.Alerting
	}
	readCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	results, err := reader.Recent(readCtx, c.Name, replayLimit)
	if err != nil {
		return streak.Alerting
	}
	for len(results) > 0 && config.EvaluateMaintenance(ws, results[0].At).Active {
		results = results[1:]
	}
	return check.Replay(results, c.AlertAfter, c.RecoverAfter).Alerting
}
