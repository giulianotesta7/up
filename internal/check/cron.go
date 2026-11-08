package check

import (
	"time"

	"github.com/heohlee/up/internal/config"
)

// A cron heartbeat (config.ScheduleCron) expects a ping for every run of its
// cron expression in the Defaults zone and is missed Grace after a run
// without one. Every result counts for one run:
//
//   - a ping or a reported failure counts for the latest run that has
//     already started, or for a run starting within CronEarlyTolerance (the
//     sender's clock may be ahead), so several pings in one run count once
//     and a ping after its run already has one changes nothing: it never
//     counts for a later run. One that arrives while an older run still owed
//     a ping has started and is within its grace counts for that run, so a
//     grace longer than the time between runs does not skip it;
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
		if Missed(r) {
			if t := cron.Prev(r.At.Add(-grace), loc); t.After(covered) {
				run = t
			}
		} else if t := pingRun(cron, grace, covered, r.At, loc); t.After(covered) {
			run = t
		}
		if run.IsZero() {
			continue
		}
		covered = run
		each(r, run)
	}
}

// CronEarlyTolerance is how long before a run a ping still counts for it, for
// a sender whose clock is slightly ahead.
const CronEarlyTolerance = 5 * time.Minute

// pingRun is the run a ping at at counts for when covered is the newest run
// with a result (zero before the first): the oldest run owed a ping that has
// started and is still within its grace, else a run starting within
// CronEarlyTolerance when it is nearer than the one already started, else the
// latest run that has started. The caller ignores a run not after covered.
func pingRun(cron config.Cron, grace time.Duration, covered, at time.Time, loc *time.Location) time.Time {
	prev := cron.Prev(at, loc)
	owed := prev
	if !covered.IsZero() {
		owed = cron.Next(covered, loc)
	}
	if !owed.IsZero() && !owed.After(at) && !at.After(owed.Add(grace)) {
		return owed
	}
	if next := cron.Next(at, loc); !next.IsZero() {
		if early := next.Sub(at); early <= CronEarlyTolerance && (prev.IsZero() || early < at.Sub(prev)) {
			return next
		}
	}
	return prev
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
