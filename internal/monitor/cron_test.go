package monitor

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
)

// cronRig turns the rig's heartbeat into a cron one at 06:00 -03 on
// weekdays (09:00 UTC, when beatStart is), grace 10m, in the Defaults zone
// America/Argentina/Buenos_Aires. beatStart is Monday 2030-01-07.
func cronRig(t *testing.T, alertAfter int) *beatRig {
	t.Helper()
	r := newBeatRig(t, alertAfter, 1)
	ctx := context.Background()
	d := config.BuiltinDefaults()
	d.Zone = "America/Argentina/Buenos_Aires"
	if err := r.s.SetDefaults(ctx, d); err != nil {
		t.Fatal(err)
	}
	c := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "0 6 * * 1-5", Grace: 10 * time.Minute, Channel: "mail", AlertAfter: alertAfter}
	if err := r.s.UpdateCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	return r
}

// missedRun is a cron miss stored at at for its run.
func missedRun(at, run time.Time, reason string) check.Result {
	r := missedAt(at, reason)
	r.Run = run
	return r
}

func day(n int, hour, min, sec int) time.Time {
	return time.Date(2030, 1, 7+n, hour, min, sec, 0, time.UTC)
}

func TestCronHeartbeatMissesARunAfterItsGrace(t *testing.T) {
	r := cronRig(t, 1)
	r.start()
	r.ping(day(0, 9, 0, 30), false)
	r.ping(day(0, 9, 2, 0), false) // a second ping in the same run
	r.waitResults(2)

	// Tuesday's run is due by 09:10 UTC: nothing at the deadline, a miss
	// just after it.
	r.clock.setAt(day(1, 9, 10, 0))
	time.Sleep(60 * time.Millisecond)
	if got := r.results(); len(got) != 2 {
		t.Fatalf("results at the deadline = %+v", got)
	}
	r.clock.setAt(day(1, 9, 10, 1))
	got := r.waitResults(3)
	if want := missedRun(day(1, 9, 10, 0), day(1, 9, 0, 0), "no ping for the 06:00 -03 run"); got[0] != want {
		t.Fatalf("missed = %+v, want %+v", got[0], want)
	}
	r.waitAlerts(down)

	// A late ping recovers; Wednesday's early ping counts for Wednesday.
	r.ping(day(1, 9, 20, 0), false)
	r.waitAlerts(down, up)
	r.ping(day(2, 8, 55, 0), false)
	r.clock.setAt(day(2, 9, 11, 0))
	time.Sleep(60 * time.Millisecond)
	if got := r.results(); len(got) != 5 {
		t.Fatalf("results after an early ping = %+v", got)
	}

	// Friday's ping covers the weekend: no runs, no misses until Monday's.
	r.ping(day(3, 9, 0, 0), false)
	r.ping(day(4, 9, 1, 0), false)
	for _, at := range []time.Time{day(5, 9, 11, 0), day(6, 9, 11, 0), day(7, 9, 10, 0)} {
		r.clock.setAt(at)
		time.Sleep(40 * time.Millisecond)
	}
	if got := r.results(); len(got) != 7 {
		t.Fatalf("results over the weekend = %+v", got)
	}
	r.waitAlerts(down, up)
	r.clock.setAt(day(7, 9, 10, 1))
	got = r.waitResults(8)
	if want := missedRun(day(7, 9, 10, 0), day(7, 9, 0, 0), "no ping for the 06:00 -03 run"); got[0] != want {
		t.Fatalf("Monday's miss = %+v, want %+v", got[0], want)
	}
	r.waitAlerts(down, up, down)
}

// The daemon stopped across several runs: one outage, recorded as the first
// and the latest missed run, which reaches alert after 2.
func TestCronHeartbeatDownAcrossRunsRecordsOneOutage(t *testing.T) {
	r := cronRig(t, 2)
	r.start()
	r.ping(day(0, 9, 0, 30), false)
	r.waitResults(1)
	r.stop()
	r.clock.setAt(day(3, 12, 0, 0)) // Thursday
	r.start()
	got := r.waitResults(3)
	want := []check.Result{
		missedRun(day(3, 9, 10, 0), day(3, 9, 0, 0), "no ping for the 06:00 -03 run"),
		missedRun(day(1, 9, 10, 0), day(1, 9, 0, 0), "no ping for the 06:00 -03 run"),
		check.Ping("backup", day(0, 9, 0, 30)),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results after restart = %+v, want %+v", got, want)
	}
	r.waitAlerts(down)
	outages, err := r.s.Outages(context.Background(), "backup", 10)
	if err != nil || len(outages) != 1 || !outages[0].Ongoing {
		t.Fatalf("Outages() = %+v, %v, want one ongoing", outages, err)
	}
}

// Daylight saving in the Defaults zone: in New York the 02:30 run of
// 2030-03-10 does not exist and cron runs it at 03:00 EDT (07:00 UTC).
func TestCronHeartbeatAcrossTheSpringJump(t *testing.T) {
	r := newBeatRig(t, 1, 1)
	ctx := context.Background()
	d := config.BuiltinDefaults()
	d.Zone = "America/New_York"
	if err := r.s.SetDefaults(ctx, d); err != nil {
		t.Fatal(err)
	}
	c := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "30 2 * * *", Grace: 15 * time.Minute, Channel: "mail"}
	if err := r.s.UpdateCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.ping(time.Date(2030, 3, 9, 7, 31, 0, 0, time.UTC), false) // Mar 9 02:31 EST
	r.waitResults(1)
	// No false alert at 02:45 EST would-be time or before the jump's deadline.
	for _, at := range []time.Time{time.Date(2030, 3, 10, 6, 46, 0, 0, time.UTC), time.Date(2030, 3, 10, 7, 15, 0, 0, time.UTC)} {
		r.clock.setAt(at)
		time.Sleep(40 * time.Millisecond)
	}
	if got := r.results(); len(got) != 1 {
		t.Fatalf("results before the deadline = %+v", got)
	}
	// Not hidden either: without a ping the run is missed once.
	r.clock.setAt(time.Date(2030, 3, 10, 7, 15, 1, 0, time.UTC))
	got := r.waitResults(2)
	if want := missedRun(time.Date(2030, 3, 10, 7, 15, 0, 0, time.UTC), time.Date(2030, 3, 10, 7, 0, 0, 0, time.UTC), "no ping for the 03:00 EDT run"); got[0] != want {
		t.Fatalf("missed = %+v, want %+v", got[0], want)
	}
	r.waitAlerts(down)
}
