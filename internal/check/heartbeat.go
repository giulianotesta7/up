package check

import (
	"strings"
	"time"

	"github.com/heohlee/up/internal/config"
)

// Reasons of a heartbeat's down results. A missed deadline's reason starts
// with missedPrefix and names the last ping (or reported failure) it knows,
// or for a cron heartbeat the run it missed.
const (
	ReasonReportedFailure = "reported failure"
	missedPrefix          = "no ping"
)

// Ping is the result of a heartbeat's job reporting success at at.
func Ping(name string, at time.Time) Result {
	return Result{CheckName: name, At: at, Status: StatusUp}
}

// Fail is the result of a heartbeat's job reporting failure at at.
func Fail(name string, at time.Time) Result {
	return Result{CheckName: name, At: at, Status: StatusDown, Error: ReasonReportedFailure}
}

// Failed reports whether r is a heartbeat failure reported by its job.
func Failed(r Result) bool { return r.Status == StatusDown && r.Error == ReasonReportedFailure }

// Missed reports whether r is a heartbeat deadline that passed without a ping.
func Missed(r Result) bool {
	return r.Status == StatusDown && strings.HasPrefix(r.Error, missedPrefix)
}

// NextDeadline is when a heartbeat whose newest result is at newest is down
// without another ping: Period + Grace later. Every result (ping, failure or
// missed deadline) starts a new period, so a job that stays silent is missed
// once per Period + Grace.
func NextDeadline(c config.Check, newest time.Time) time.Time {
	return newest.Add(c.Period + c.Grace)
}

// Overdue returns the missed-deadline results due at now, given the newest
// result's time and the last time the job reported (a ping or a failure;
// zero when unknown). The reason names that time in loc (nil is UTC). A check is missed once now is after a deadline. When
// several deadlines passed (the daemon was stopped), it returns only two:
// the first, where the outage starts, and the latest, so the alert counts
// see more than one miss without filling the history.
func Overdue(c config.Check, newest, contact, now time.Time, loc *time.Location) []Result {
	step := c.Period + c.Grace
	if step <= 0 || !now.After(newest.Add(step)) {
		return nil
	}
	n := int64((now.Sub(newest) - 1) / step)
	at := []time.Time{newest.Add(step)}
	if n >= 2 {
		at = append(at, newest.Add(time.Duration(n)*step))
	}
	out := make([]Result, len(at))
	for i, t := range at {
		out[i] = Result{CheckName: c.Name, At: t, Status: StatusDown, Error: missedReason(contact, t, loc)}
	}
	return out
}

// missedReason names the last contact in loc (nil is UTC) with the zone's
// abbreviation, and its date when it was on another day than the deadline
// in that zone.
func missedReason(contact, deadline time.Time, loc *time.Location) string {
	if contact.IsZero() {
		return missedPrefix
	}
	if loc == nil {
		loc = time.UTC
	}
	contact, deadline = contact.In(loc), deadline.In(loc)
	layout := "15:04 MST"
	if y, m, d := contact.Date(); y != deadline.Year() || m != deadline.Month() || d != deadline.Day() {
		layout = "Jan 2 15:04 MST"
	}
	return missedPrefix + " since " + contact.Format(layout)
}
