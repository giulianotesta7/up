package monitor

import (
	"testing"
	"time"
)

// S4(2) through the monitor with an injected clock: a manual ping at 14:01
// -03 on Tuesday does not hide Wednesday's 06:00 -03 miss, and a ping 10
// minutes before Thursday's run does not count for it.
func TestProbeCronManualPingDoesNotHideTheNextMiss(t *testing.T) {
	r := cronRig(t, 1)
	r.start()
	r.ping(day(0, 9, 0, 30), false) // Mon 06:00:30 -03
	r.ping(day(1, 9, 5, 0), false)  // Tue 06:05 -03
	r.ping(day(1, 17, 1, 0), false) // Tue 14:01 -03, manual
	r.waitResults(3)
	r.clock.setAt(day(2, 9, 10, 0))
	time.Sleep(60 * time.Millisecond)
	if got := r.results(); len(got) != 3 {
		t.Fatalf("results at Wednesday's deadline = %+v", got)
	}
	r.clock.setAt(day(2, 9, 10, 1))
	got := r.waitResults(4)
	if want := missedAt(day(2, 9, 10, 0), "no ping for the 06:00 -03 run"); got[0] != want {
		t.Fatalf("Wednesday = %+v, want %+v", got[0], want)
	}
	r.waitAlerts(down)

	r.ping(day(3, 8, 50, 0), false) // Thu 05:50 -03: too early for Thursday
	r.waitResults(5)
	r.waitAlerts(down, up)
	r.clock.setAt(day(3, 9, 10, 1))
	got = r.waitResults(6)
	if want := missedAt(day(3, 9, 10, 0), "no ping for the 06:00 -03 run"); got[0] != want {
		t.Fatalf("Thursday = %+v, want %+v", got[0], want)
	}
	r.waitAlerts(down, up, down)

	r.ping(day(4, 8, 58, 0), false) // Fri 05:58 -03 counts for Friday
	r.waitResults(7)
	r.clock.setAt(day(4, 9, 11, 0))
	time.Sleep(60 * time.Millisecond)
	if got := r.results(); len(got) != 7 {
		t.Fatalf("results after Friday's early ping = %+v", got)
	}
}
