package check

import (
	"time"

	"github.com/heohlee/up/internal/config"
)

// A cron heartbeat (config.ScheduleCron) expects a ping for every run of its
// cron expression in the Defaults zone and is missed Grace after a run
// without one. Every result counts for one run:
//
//   - a ping or a reported failure counts for its nearest run, so an early
//     ping counts for the run it precedes and several pings in one run count
//     once; one that arrives while the oldest run still owed a ping is within
//     its grace counts for that run, even when it is nearer the next one;
//   - a missed result counts for the run it was recorded for: it is stored
//     at that run + Grace.
//
// The results replayed are the ones given (newest first, as stored), so the
// state survives restarts and follows edits like a period heartbeat's.

// Deadline is when a heartbeat whose newest-first results are recent is
// missed without another result, with its runs in loc (nil is UTC): for a
// period heartbeat the newest result + Period + Grace (NextDeadline), for a
// cron one the next run owed a ping + Grace. Times are in the location of
// the newest result. Zero before the first result (WAITING) and for a cron
// expression that does not parse.
func Deadline(c config.Check, recent []Result, loc *time.Location) time.Time {
	if len(recent) == 0 {
		return time.Time{}
	}
	if c.Schedule != config.ScheduleCron {
		return NextDeadline(c, recent[0].At)
	}
	cron, covered, ok := cronState(c, recent, loc)
	if !ok {
		return time.Time{}
	}
	next := cron.Next(covered, loc)
	if next.IsZero() {
		return time.Time{}
	}
	return next.Add(c.Grace).In(recent[0].At.Location())
}

// Due returns the missed results due at now given the newest-first results
// recent, with runs and reasons in loc (nil is UTC). A period heartbeat's are
// Overdue's, naming the last ping or reported failure. A cron heartbeat's are
// stored at each missed run + Grace with the reason `no ping for the 02:00
// -03 run` (the run's date too when it is not on its deadline's day). When
// several runs were missed (the daemon was stopped) it returns only the first,
// where the outage starts, and the latest, as a period heartbeat does.
func Due(c config.Check, recent []Result, now time.Time, loc *time.Location) []Result {
	if len(recent) == 0 {
		return nil
	}
	if c.Schedule != config.ScheduleCron {
		var contact time.Time
		for _, r := range recent {
			if !Missed(r) {
				contact = r.At
				break
			}
		}
		return Overdue(c, recent[0].At, contact, now, loc)
	}
	if loc == nil {
		loc = time.UTC
	}
	cron, covered, ok := cronState(c, recent, loc)
	if !ok {
		return nil
	}
	first := cron.Next(covered, loc)
	if first.IsZero() || !now.After(first.Add(c.Grace)) {
		return nil
	}
	runs := []time.Time{first}
	if latest := cron.Prev(now.Add(-c.Grace-time.Nanosecond), loc); latest.After(first) {
		runs = append(runs, latest)
	}
	out := make([]Result, len(runs))
	for i, run := range runs {
		at := run.Add(c.Grace).In(recent[0].At.Location())
		out[i] = Result{CheckName: c.Name, At: at, Status: StatusDown, Error: missedRunReason(run, at, loc)}
	}
	return out
}

// RunResults is one result per expected run, newest first: for a cron
// heartbeat the newest result of each run that has any (a run without a
// result has no cell, so days without runs add none); a period heartbeat's
// results are returned as they are, one per period.
func RunResults(c config.Check, recent []Result, loc *time.Location) []Result {
	if c.Schedule != config.ScheduleCron {
		return recent
	}
	cron, err := config.ParseCron(c.Cron)
	if err != nil {
		return nil
	}
	var out []Result
	var last time.Time
	replayRuns(cron, c.Grace, recent, loc, func(r Result, run time.Time) {
		if len(out) > 0 && run.Equal(last) {
			out[len(out)-1] = r
			return
		}
		out = append(out, r)
		last = run
	})
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// cronState parses the heartbeat's expression and returns the newest run
// its results cover.
func cronState(c config.Check, recent []Result, loc *time.Location) (config.Cron, time.Time, bool) {
	cron, err := config.ParseCron(c.Cron)
	if err != nil {
		return config.Cron{}, time.Time{}, false
	}
	var covered time.Time
	replayRuns(cron, c.Grace, recent, loc, func(_ Result, run time.Time) { covered = run })
	return cron, covered, !covered.IsZero()
}

// replayRuns calls each with every result of the newest-first recent, oldest
// first, and the run it counts for, which never goes back.
func replayRuns(cron config.Cron, grace time.Duration, recent []Result, loc *time.Location, each func(Result, time.Time)) {
	var covered time.Time
	for i := len(recent) - 1; i >= 0; i-- {
		r := recent[i]
		run := covered
		switch {
		case Missed(r):
			if t := cron.Prev(r.At.Add(-grace), loc); t.After(covered) {
				run = t
			}
		default:
			near := nearestRun(cron, r.At, loc)
			switch {
			case covered.IsZero():
				run = near
			case !near.After(covered):
			default:
				// The oldest run still owed a ping takes a ping within its grace.
				if owed := cron.Next(covered, loc); !r.At.After(owed.Add(grace)) {
					run = owed
				} else {
					run = near
				}
			}
		}
		if run.IsZero() {
			continue
		}
		covered = run
		each(r, run)
	}
}

// nearestRun is the run nearest at; the earlier one on a tie.
func nearestRun(cron config.Cron, at time.Time, loc *time.Location) time.Time {
	prev, next := cron.Prev(at, loc), cron.Next(at, loc)
	switch {
	case prev.IsZero():
		return next
	case next.IsZero() || at.Sub(prev) <= next.Sub(at):
		return prev
	}
	return next
}

// missedRunReason names the missed run in loc with the zone's abbreviation,
// and its date when it is on another day than its deadline in that zone.
func missedRunReason(run, deadline time.Time, loc *time.Location) string {
	run, deadline = run.In(loc), deadline.In(loc)
	layout := "15:04 MST"
	if y, m, d := run.Date(); y != deadline.Year() || m != deadline.Month() || d != deadline.Day() {
		layout = "Jan 2 15:04 MST"
	}
	return missedPrefix + " for the " + run.Format(layout) + " run"
}
