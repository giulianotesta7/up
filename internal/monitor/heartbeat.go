package monitor

import (
	"context"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

// MissedRecorder is the optional API that stores a heartbeat's missed
// deadline only while no newer result exists, so a ping that arrives while
// the deadline is being recorded wins. *store.Store implements it.
type MissedRecorder interface {
	RecordMissed(ctx context.Context, c config.Check, newest time.Time, r check.Result) (bool, error)
}

// watchHeartbeat follows a heartbeat until ctx ends. It makes no outbound
// check: pings and reported failures are stored by the API, and every
// discovery interval it observes the new ones, then records a missed
// deadline when the clock is past the newest result + period + grace (see
// check.Overdue). Before the first result (WAITING) there is no deadline.
// The deadline comes from stored results, so it survives restarts and
// follows edits. idle rechecks held alerts when nothing new arrived.
func watchHeartbeat(ctx context.Context, opts Options, c config.Check, observe func(check.Result), idle func()) {
	poll := time.NewTicker(opts.DiscoveryInterval)
	defer poll.Stop()
	// seen is the newest result observed and contact the last ping or
	// reported failure, which the missed reason names.
	var seen, contact time.Time
	initial := recentResults(ctx, opts, c)
	if len(initial) > 0 {
		seen = initial[0].At
		contact = lastContact(initial)
	}
	for {
		fresh := 0
		results := recentResults(ctx, opts, c)
		for i := len(results) - 1; i >= 0; i-- {
			r := results[i]
			if !r.At.After(seen) || ctx.Err() != nil {
				continue
			}
			fresh++
			observe(r)
			seen = r.At
			if !check.Missed(r) {
				contact = r.At
			}
		}
		if !seen.IsZero() && ctx.Err() == nil {
			for _, r := range check.Overdue(c, seen, contact, opts.now()) {
				if !recordMissed(ctx, opts, c, seen, r) {
					break
				}
				fresh++
				observe(r)
				seen = r.At
			}
		}
		if fresh == 0 {
			idle()
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
		}
	}
}

// recentResults reads the heartbeat's latest results, newest first. A read
// error is logged and counts as none new.
func recentResults(ctx context.Context, opts Options, c config.Check) []check.Result {
	reader, ok := opts.Store.(RecentReader)
	if !ok {
		return nil
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	results, err := reader.Recent(readCtx, c.Name, replayLimit)
	if err != nil {
		opts.Logger.Error("reading recent results failed", "check", c.Name, "error", err)
		return nil
	}
	return results
}

// lastContact is the newest ping or reported failure among newest-first
// results, or zero.
func lastContact(results []check.Result) time.Time {
	for _, r := range results {
		if !check.Missed(r) {
			return r.At
		}
	}
	return time.Time{}
}

// recordMissed stores a missed deadline computed from the result at newest
// and reports whether it was stored.
func recordMissed(ctx context.Context, opts Options, c config.Check, newest time.Time, r check.Result) bool {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	var stored bool
	var err error
	if recorder, ok := opts.Store.(MissedRecorder); ok {
		stored, err = recorder.RecordMissed(wctx, c, newest, r)
	} else {
		stored, err = true, opts.Store.Record(wctx, r)
	}
	if err != nil {
		opts.Logger.Error("storing result failed", "check", c.Name, "error", err)
		return false
	}
	return stored
}
