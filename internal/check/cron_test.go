package check

import (
	"reflect"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// weekdays is a cron heartbeat for a job at 02:00 on weekdays, 30m grace.
var weekdays = config.Check{Name: "backup", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "0 2 * * 1-5", Grace: 30 * time.Minute}

// newestFirst reverses results given oldest first.
func newestFirst(oldest ...Result) []Result {
	out := make([]Result, len(oldest))
	for i, r := range oldest {
		out[len(oldest)-1-i] = r
	}
	return out
}

// sameResults compares results with times as instants, whatever their zone.
func sameResults(a, b []Result) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if !x.At.Equal(y.At) {
			return false
		}
		x.At, y.At = time.Time{}, time.Time{}
		if x != y {
			return false
		}
	}
	return true
}

func miss(at time.Time, reason string) Result {
	return Result{CheckName: "backup", At: at, Status: StatusDown, Error: reason}
}

func TestCronDeadlineAndDue(t *testing.T) {
	ba := zone(t, "America/Argentina/Buenos_Aires")
	at := func(day, hour, min int) time.Time { return time.Date(2026, 11, day, hour, min, 0, 0, ba) }
	// Monday Nov 9 to Monday Nov 16 2026.
	mon := Ping("backup", at(9, 2, 5))
	tuesday := at(10, 2, 30)
	tests := []struct {
		name     string
		results  []Result
		now      time.Time
		deadline time.Time
		due      []Result
	}{
		{"waiting", nil, at(20, 0, 0), time.Time{}, nil},
		{"before the next run's deadline", newestFirst(mon), tuesday.Add(-time.Second), tuesday, nil},
		{"at the deadline", newestFirst(mon), tuesday, tuesday, nil},
		{"just after the deadline", newestFirst(mon), tuesday.Add(time.Nanosecond), tuesday,
			[]Result{miss(tuesday, "no ping for the 02:00 -03 run")}},
		{"a late ping within grace counts", newestFirst(mon, Ping("backup", at(10, 2, 29))), at(10, 23, 0), at(11, 2, 30), nil},
		{"a ping up to five minutes early counts for its run", newestFirst(mon, Ping("backup", at(10, 1, 55))), at(10, 23, 0), at(11, 2, 30), nil},
		{"an earlier ping does not", newestFirst(mon, Ping("backup", at(10, 1, 50))), tuesday.Add(time.Second), tuesday,
			[]Result{miss(tuesday, "no ping for the 02:00 -03 run")}},
		{"several pings in one run count once", newestFirst(mon, Ping("backup", at(9, 2, 10)), Ping("backup", at(9, 2, 20))), tuesday.Add(time.Second), tuesday,
			[]Result{miss(tuesday, "no ping for the 02:00 -03 run")}},
		{"a reported failure counts for its run", newestFirst(mon, Fail("backup", at(10, 2, 3))), at(10, 23, 0), at(11, 2, 30), nil},
		{"the weekend has no runs", newestFirst(Ping("backup", at(13, 2, 1))), at(16, 2, 0), at(16, 2, 30), nil},
		{"after a miss the next run is due", newestFirst(mon, miss(tuesday, "no ping for the 02:00 -03 run")), at(11, 2, 31), at(11, 2, 30),
			[]Result{miss(at(11, 2, 30), "no ping for the 02:00 -03 run")}},
		{"a ping after a miss counts for the missed run", newestFirst(mon, miss(tuesday, "no ping for the 02:00 -03 run"), Ping("backup", at(10, 2, 40))), at(11, 2, 0), at(11, 2, 30), nil},
		{"a daemon down across runs records the first and the latest", newestFirst(mon), at(13, 12, 0), tuesday,
			[]Result{miss(tuesday, "no ping for the 02:00 -03 run"), miss(at(13, 2, 30), "no ping for the 02:00 -03 run")}},
		{"two runs missed", newestFirst(mon), at(11, 2, 31), tuesday,
			[]Result{miss(tuesday, "no ping for the 02:00 -03 run"), miss(at(11, 2, 30), "no ping for the 02:00 -03 run")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Deadline(weekdays, tt.results, ba); !got.Equal(tt.deadline) {
				t.Fatalf("Deadline() = %v, want %v", got, tt.deadline)
			}
			got := Due(weekdays, tt.results, tt.now, ba)
			if !sameResults(got, tt.due) {
				t.Fatalf("Due() = %+v, want %+v", got, tt.due)
			}
			for _, r := range got {
				if !Missed(r) || Failed(r) {
					t.Fatalf("%+v is not a missed result", r)
				}
			}
		})
	}
}

func TestCronMissedReason(t *testing.T) {
	ba := zone(t, "America/Argentina/Buenos_Aires")
	late := weekdays
	late.Cron, late.Grace = "30 23 * * *", time.Hour
	ping := Ping("backup", time.Date(2026, 11, 8, 23, 31, 0, 0, ba))
	deadline := time.Date(2026, 11, 10, 0, 30, 0, 0, ba)
	// The run is on the day before its deadline, so the reason names its date.
	if got, want := Due(late, []Result{ping}, deadline.Add(time.Second), ba), []Result{miss(deadline, "no ping for the Nov 9 23:30 -03 run")}; !sameResults(got, want) {
		t.Fatalf("Due() = %+v, want %+v", got, want)
	}
	// A nil zone is UTC, and the run is computed in it.
	utcPing := Ping("backup", time.Date(2026, 11, 9, 2, 1, 0, 0, time.UTC))
	if got, want := Due(weekdays, []Result{utcPing}, time.Date(2026, 11, 10, 2, 31, 0, 0, time.UTC), nil), []Result{miss(time.Date(2026, 11, 10, 2, 30, 0, 0, time.UTC), "no ping for the 02:00 UTC run")}; !sameResults(got, want) {
		t.Fatalf("Due() in UTC = %+v, want %+v", got, want)
	}
}

// New York: 2026-03-08 at 02:00 EST the clock jumps to 03:00 EDT, and
// 2026-11-01 at 02:00 EDT it falls back to 01:00 EST. Neither raises a false
// alert nor hides a missed run.
func TestCronDaylightSavingDeadlines(t *testing.T) {
	ny := zone(t, "America/New_York")
	c := weekdays
	c.Cron, c.Grace = "30 2 * * *", 15*time.Minute
	utc := func(month time.Month, day, hour, min int) time.Time {
		return time.Date(2026, month, day, hour, min, 0, 0, time.UTC)
	}

	// Spring: the 02:30 run does not exist on Mar 8; cron runs it at 03:00
	// EDT (07:00 UTC).
	before := []Result{Ping("backup", utc(3, 7, 7, 31))} // Mar 7 02:31 EST
	jump := utc(3, 8, 7, 0)
	if got := Deadline(c, before, ny); !got.Equal(jump.Add(15 * time.Minute)) {
		t.Fatalf("spring Deadline() = %v, want %v", got, jump.Add(15*time.Minute))
	}
	// No false alert: up to the deadline nothing is due, and the job's ping
	// at 03:01 EDT counts for that run.
	if got := Due(c, before, jump.Add(15*time.Minute), ny); got != nil {
		t.Fatalf("spring Due() at the deadline = %+v", got)
	}
	pinged := append([]Result{Ping("backup", jump.Add(time.Minute))}, before...)
	if got := Due(c, pinged, utc(3, 9, 6, 0), ny); got != nil {
		t.Fatalf("spring Due() after the ping = %+v", got)
	}
	if got := Deadline(c, pinged, ny); !got.Equal(utc(3, 9, 6, 45)) { // Mar 9 02:45 EDT
		t.Fatalf("spring Deadline() after the ping = %v", got)
	}
	// Not hidden: without the ping the run is missed once.
	if got, want := Due(c, before, jump.Add(16*time.Minute), ny), []Result{miss(jump.Add(15*time.Minute), "no ping for the 03:00 EDT run")}; !sameResults(got, want) {
		t.Fatalf("spring Due() = %+v, want %+v", got, want)
	}

	// Fall: 01:30 happens twice on Nov 1; cron runs the job once, the first
	// time (05:30 UTC).
	c.Cron = "30 1 * * *"
	before = []Result{Ping("backup", utc(10, 31, 5, 31))} // Oct 31 01:31 EDT
	first := utc(11, 1, 5, 30)
	if got := Deadline(c, before, ny); !got.Equal(first.Add(15 * time.Minute)) {
		t.Fatalf("fall Deadline() = %v, want %v", got, first.Add(15*time.Minute))
	}
	pinged = append([]Result{Ping("backup", first.Add(time.Minute))}, before...)
	// No false alert in the repeated hour or after it.
	for _, now := range []time.Time{utc(11, 1, 6, 31), utc(11, 1, 6, 46), utc(11, 2, 6, 45)} {
		if got := Due(c, pinged, now, ny); got != nil {
			t.Fatalf("fall Due(%v) after the ping = %+v", now, got)
		}
	}
	if got := Deadline(c, pinged, ny); !got.Equal(utc(11, 2, 6, 45)) { // Nov 2 01:45 EST
		t.Fatalf("fall Deadline() after the ping = %v", got)
	}
	// Not hidden, and missed once, not once per pass.
	want := []Result{miss(first.Add(15*time.Minute), "no ping for the 01:30 EDT run")}
	for _, now := range []time.Time{first.Add(16 * time.Minute), utc(11, 1, 6, 46)} {
		if got := Due(c, before, now, ny); !sameResults(got, want) {
			t.Fatalf("fall Due(%v) = %+v, want %+v", now, got, want)
		}
	}

	// An hourly job runs in both passes of the repeated hour; the second
	// pass's run is expected too.
	c.Cron, c.Grace = "30 * * * *", 10*time.Minute
	pinged = newestFirst(Ping("backup", utc(11, 1, 4, 31)), Ping("backup", utc(11, 1, 5, 31)))
	if got, want := Due(c, pinged, utc(11, 1, 6, 41), ny), []Result{miss(utc(11, 1, 6, 40), "no ping for the 01:30 EST run")}; !sameResults(got, want) {
		t.Fatalf("hourly fall Due() = %+v, want %+v", got, want)
	}
}

func TestRunResults(t *testing.T) {
	ba := zone(t, "America/Argentina/Buenos_Aires")
	at := func(day, hour, min int) time.Time { return time.Date(2026, 11, day, hour, min, 0, 0, ba) }
	results := newestFirst(
		Ping("backup", at(9, 2, 1)), Ping("backup", at(9, 2, 9)), // Monday, twice
		miss(at(10, 2, 30), "no ping for the 02:00 -03 run"), Ping("backup", at(10, 2, 40)), // Tuesday, late
		Fail("backup", at(11, 2, 2)),  // Wednesday
		Ping("backup", at(13, 1, 58)), // Friday, early
		Ping("backup", at(16, 2, 0)),  // Monday after a weekend without cells
	)
	want := newestFirst(Ping("backup", at(9, 2, 9)), Ping("backup", at(10, 2, 40)), Fail("backup", at(11, 2, 2)), Ping("backup", at(13, 1, 58)), Ping("backup", at(16, 2, 0)))
	if got := RunResults(weekdays, results, ba); !reflect.DeepEqual(got, want) {
		t.Fatalf("RunResults() =\n%+v\nwant\n%+v", got, want)
	}
	// A period heartbeat keeps one cell per result.
	if got := RunResults(beat, results, ba); !reflect.DeepEqual(got, results) {
		t.Fatalf("RunResults(period) = %+v", got)
	}
	if got := RunResults(weekdays, nil, ba); len(got) != 0 {
		t.Fatalf("RunResults(none) = %+v", got)
	}
}

// A period heartbeat goes through the same entry points unchanged.
func TestPeriodThroughDeadlineAndDue(t *testing.T) {
	ping := time.Date(2026, 11, 6, 2, 14, 0, 0, time.UTC)
	results := []Result{Ping("backup", ping)}
	if got := Deadline(beat, results, time.UTC); !got.Equal(NextDeadline(beat, ping)) {
		t.Fatalf("Deadline(period) = %v", got)
	}
	now := ping.Add(3 * time.Hour)
	if got, want := Due(beat, results, now, time.UTC), Overdue(beat, ping, ping, now, time.UTC); !reflect.DeepEqual(got, want) {
		t.Fatalf("Due(period) = %+v, want %+v", got, want)
	}
	// The reason names the last contact, not a missed deadline.
	withMiss := append([]Result{miss(ping.Add(70*time.Minute), "no ping since 02:14 UTC")}, results...)
	if got, want := Due(beat, withMiss, now, time.UTC), Overdue(beat, ping.Add(70*time.Minute), ping, now, time.UTC); !reflect.DeepEqual(got, want) {
		t.Fatalf("Due(period) after a miss = %+v, want %+v", got, want)
	}
	if Deadline(beat, nil, time.UTC) != (time.Time{}) || Due(beat, nil, now, time.UTC) != nil {
		t.Fatal("a waiting period heartbeat has a deadline")
	}
}

// A ping counts for the latest run that has already started, or for one
// starting within CronEarlyTolerance; once a run has its ping, later pings
// change nothing until the next run (or its tolerance) begins.
func TestCronPingAttribution(t *testing.T) {
	ba := zone(t, "America/Argentina/Buenos_Aires")
	at := func(day, hour, min, sec int) time.Time { return time.Date(2026, 11, day, hour, min, sec, 0, ba) }
	daily := weekdays
	daily.Cron, daily.Grace = "0 2 * * *", time.Hour
	ping := func(day, hour, min, sec int) Result { return Ping("backup", at(day, hour, min, sec)) }
	mon := ping(9, 2, 5, 0) // Monday Nov 9 2026
	tue, wed, thu := at(10, 3, 0, 0), at(11, 3, 0, 0), at(12, 3, 0, 0)
	wedMiss := miss(wed, "no ping for the 02:00 -03 run")
	tests := []struct {
		name     string
		results  []Result
		now      time.Time
		deadline time.Time
		due      []Result
	}{
		{"a ping after the run satisfies it", newestFirst(mon, ping(10, 2, 15, 0)), tue.Add(time.Second), wed, nil},
		{"a manual ping later that day does not cover the next run", newestFirst(mon, ping(10, 2, 15, 0), ping(10, 14, 1, 0)), wed.Add(time.Second), wed,
			[]Result{wedMiss}},
		{"a manual ping after a miss does not cover the next run", newestFirst(mon, miss(tue, "no ping for the 02:00 -03 run"), ping(10, 14, 1, 0)), wed.Add(time.Second), wed,
			[]Result{wedMiss}},
		{"a ping two minutes early counts for the run", newestFirst(mon, ping(10, 2, 15, 0), ping(11, 1, 58, 0)), wed.Add(time.Second), thu, nil},
		{"a ping five minutes early counts for the run", newestFirst(mon, ping(10, 2, 15, 0), ping(11, 1, 55, 0)), wed.Add(time.Second), thu, nil},
		{"a ping more than five minutes early does not", newestFirst(mon, ping(10, 2, 15, 0), ping(11, 1, 54, 59)), wed.Add(time.Second), wed,
			[]Result{wedMiss}},
		{"a ping ten minutes early does not", newestFirst(mon, ping(10, 2, 15, 0), ping(11, 1, 50, 0)), wed.Add(time.Second), wed,
			[]Result{wedMiss}},
		{"two pings in one run count once", newestFirst(mon, ping(10, 2, 10, 0), ping(10, 2, 20, 0)), wed.Add(time.Second), wed,
			[]Result{wedMiss}},
		{"a first ping early counts for the coming run", newestFirst(ping(11, 1, 58, 0)), wed.Add(time.Second), thu, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Deadline(daily, tt.results, ba); !got.Equal(tt.deadline) {
				t.Fatalf("Deadline() = %v, want %v", got, tt.deadline)
			}
			if got := Due(daily, tt.results, tt.now, ba); !sameResults(got, tt.due) {
				t.Fatalf("Due() = %+v, want %+v", got, tt.due)
			}
		})
	}

	// One trend cell per run, the run's newest result: the pings at 02:10,
	// 02:20, 14:01 and Wednesday 01:50 all count for Tuesday's run, Wednesday
	// is missed, and Thursday's early ping is Thursday's.
	results := newestFirst(mon, ping(10, 2, 10, 0), ping(10, 2, 20, 0), ping(10, 14, 1, 0), ping(11, 1, 50, 0), wedMiss, ping(12, 1, 58, 0))
	want := newestFirst(mon, ping(11, 1, 50, 0), wedMiss, ping(12, 1, 58, 0))
	if got := RunResults(daily, results, ba); !reflect.DeepEqual(got, want) {
		t.Fatalf("RunResults() =\n%+v\nwant\n%+v", got, want)
	}
	if got := Deadline(daily, results, ba); !got.Equal(at(13, 3, 0, 0)) {
		t.Fatalf("Deadline() after Thursday = %v", got)
	}
}

// When the grace is longer than the time between runs, a late ping still
// counts for the oldest run owed one rather than skipping it.
func TestCronLatePingForAnOwedRun(t *testing.T) {
	hourly := weekdays
	hourly.Cron, hourly.Grace = "0 * * * *", 2*time.Hour
	at := func(hour, min int) time.Time { return time.Date(2026, 11, 9, hour, min, 0, 0, time.UTC) }
	results := newestFirst(Ping("backup", at(10, 5)), Ping("backup", at(12, 10)))
	if got := Deadline(hourly, results, time.UTC); !got.Equal(at(14, 0)) {
		t.Fatalf("Deadline() = %v, want the 12:00 run's %v", got, at(14, 0))
	}
	if got := Due(hourly, results, at(14, 0), time.UTC); got != nil {
		t.Fatalf("Due() = %+v", got)
	}
}
