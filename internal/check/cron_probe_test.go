package check

import (
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

// Probes for issue #96 / S4(2) (L7, L8): Buenos Aires, 0 2 * * 1-5, grace 1h.
func TestProbeCronAttributionBuenosAires(t *testing.T) {
	ba := zone(t, "America/Argentina/Buenos_Aires")
	c := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "0 2 * * 1-5", Grace: time.Hour}
	at := func(day, hour, min, sec int) time.Time { return time.Date(2026, 11, day, hour, min, sec, 0, ba) } // Nov 9 is Monday
	reason := "no ping for the 02:00 -03 run"
	mon := Ping("backup", at(9, 2, 5, 0))
	tue := Ping("backup", at(10, 2, 15, 0))
	manual := Ping("backup", at(10, 14, 1, 0))
	cases := []struct {
		name     string
		results  []Result
		now      time.Time
		deadline time.Time
		due      []Result
	}{
		{"Tue 02:15 satisfies Tue", newestFirst(mon, tue), at(10, 3, 0, 1), at(11, 3, 0, 0), nil},
		{"manual Tue 14:01 does not cover Wed", newestFirst(mon, tue, manual), at(11, 3, 0, 1), at(11, 3, 0, 0), []Result{miss(at(11, 3, 0, 0), reason)}},
		{"manual Tue 14:01 after a recorded Tue miss does not cover Wed", newestFirst(mon, miss(at(10, 3, 0, 0), reason), manual), at(11, 3, 0, 1), at(11, 3, 0, 0), []Result{miss(at(11, 3, 0, 0), reason)}},
		{"Wed 01:58 counts for Wed", newestFirst(mon, tue, Ping("backup", at(11, 1, 58, 0))), at(11, 3, 0, 1), at(12, 3, 0, 0), nil},
		{"Wed 01:55:00 counts for Wed (boundary)", newestFirst(mon, tue, Ping("backup", at(11, 1, 55, 0))), at(11, 3, 0, 1), at(12, 3, 0, 0), nil},
		{"Wed 01:54:59 does not", newestFirst(mon, tue, Ping("backup", at(11, 1, 54, 59))), at(11, 3, 0, 1), at(11, 3, 0, 0), []Result{miss(at(11, 3, 0, 0), reason)}},
		{"Wed 01:50 does not", newestFirst(mon, tue, Ping("backup", at(11, 1, 50, 0))), at(11, 3, 0, 1), at(11, 3, 0, 0), []Result{miss(at(11, 3, 0, 0), reason)}},
		{"Fri to Mon: Sat", newestFirst(Ping("backup", at(13, 2, 10, 0))), at(14, 12, 0, 0), at(16, 3, 0, 0), nil},
		{"Fri to Mon: Sun", newestFirst(Ping("backup", at(13, 2, 10, 0))), at(15, 23, 59, 0), at(16, 3, 0, 0), nil},
		{"Fri to Mon: Mon at deadline", newestFirst(Ping("backup", at(13, 2, 10, 0))), at(16, 3, 0, 0), at(16, 3, 0, 0), nil},
		{"Fri to Mon: Mon after deadline, one miss", newestFirst(Ping("backup", at(13, 2, 10, 0))), at(16, 3, 0, 1), at(16, 3, 0, 0), []Result{miss(at(16, 3, 0, 0), reason)}},
		{"Fri to Mon: Mon 01:57 counts", newestFirst(Ping("backup", at(13, 2, 10, 0)), Ping("backup", at(16, 1, 57, 0))), at(16, 3, 0, 1), at(17, 3, 0, 0), nil},
		{"Fri ping late within grace on Sat? no: Sat 02:00 manual ping covers nothing new", newestFirst(Ping("backup", at(13, 2, 10, 0)), Ping("backup", at(14, 2, 0, 0))), at(16, 3, 0, 1), at(16, 3, 0, 0), []Result{miss(at(16, 3, 0, 0), reason)}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := Deadline(c, tt.results, ba); !got.Equal(tt.deadline) {
				t.Fatalf("Deadline() = %v, want %v", got, tt.deadline)
			}
			if got := Due(c, tt.results, tt.now, ba); !sameResults(got, tt.due) {
				t.Fatalf("Due() = %+v, want %+v", got, tt.due)
			}
		})
	}
	// Trend: Fri, then Mon; the weekend adds no cells.
	cells := RunResults(c, newestFirst(Ping("backup", at(13, 2, 10, 0)), Ping("backup", at(13, 2, 20, 0)), Ping("backup", at(16, 2, 1, 0))), ba)
	if len(cells) != 2 || !cells[0].At.Equal(at(16, 2, 1, 0)) || !cells[1].At.Equal(at(13, 2, 20, 0)) {
		t.Fatalf("RunResults() = %+v", cells)
	}
}

func TestProbeCronDST(t *testing.T) {
	type tc struct {
		name, zone, cron string
		grace            time.Duration
		results          []Result // oldest first
		now              time.Time
		deadline         time.Time
		due              []Result
	}
	u := func(y int, m time.Month, d, h, mi int) time.Time { return time.Date(y, m, d, h, mi, 0, 0, time.UTC) }
	cases := []tc{
		// New York spring 2026-03-08 02:00 EST -> 03:00 EDT; 02:30 does not exist and runs at 03:00 EDT = 07:00Z.
		{"NY skipped: no false alert before jump deadline", "America/New_York", "30 2 * * *", 15 * time.Minute,
			[]Result{Ping("backup", u(2026, 3, 7, 7, 31))}, u(2026, 3, 8, 7, 15), u(2026, 3, 8, 7, 15), nil},
		{"NY skipped: ping at 03:05 EDT counts", "America/New_York", "30 2 * * *", 15 * time.Minute,
			[]Result{Ping("backup", u(2026, 3, 7, 7, 31)), Ping("backup", u(2026, 3, 8, 7, 5))}, u(2026, 3, 8, 12, 0), u(2026, 3, 9, 6, 45), nil},
		{"NY skipped: miss not hidden", "America/New_York", "30 2 * * *", 15 * time.Minute,
			[]Result{Ping("backup", u(2026, 3, 7, 7, 31))}, u(2026, 3, 8, 7, 16), u(2026, 3, 8, 7, 15),
			[]Result{miss(u(2026, 3, 8, 7, 15), "no ping for the 03:00 EDT run")}},
		// New York fall 2026-11-01 02:00 EDT -> 01:00 EST; 01:30 happens twice; runs once (first, 05:30Z).
		{"NY repeated: one ping covers it, no false alert in second pass", "America/New_York", "30 1 * * *", 15 * time.Minute,
			[]Result{Ping("backup", u(2026, 10, 31, 5, 31)), Ping("backup", u(2026, 11, 1, 5, 31))}, u(2026, 11, 1, 6, 50), u(2026, 11, 2, 6, 45), nil},
		{"NY repeated: second-pass ping counts once too", "America/New_York", "30 1 * * *", 15 * time.Minute,
			[]Result{Ping("backup", u(2026, 10, 31, 5, 31)), Ping("backup", u(2026, 11, 1, 5, 31)), Ping("backup", u(2026, 11, 1, 6, 31))}, u(2026, 11, 2, 6, 46), u(2026, 11, 2, 6, 45),
			[]Result{miss(u(2026, 11, 2, 6, 45), "no ping for the 01:30 EST run")}},
		{"NY repeated: miss not hidden", "America/New_York", "30 1 * * *", 15 * time.Minute,
			[]Result{Ping("backup", u(2026, 10, 31, 5, 31))}, u(2026, 11, 1, 5, 46), u(2026, 11, 1, 5, 45),
			[]Result{miss(u(2026, 11, 1, 5, 45), "no ping for the 01:30 EDT run")}},
		// Hourly across fall-back follows the clock: 01:00 EDT and 01:00 EST both run.
		{"NY hourly repeated hour: second pass owed", "America/New_York", "0 * * * *", 10 * time.Minute,
			[]Result{Ping("backup", u(2026, 11, 1, 5, 1))}, u(2026, 11, 1, 6, 11), u(2026, 11, 1, 6, 10),
			[]Result{miss(u(2026, 11, 1, 6, 10), "no ping for the 01:00 EST run")}},
		// Hourly across spring: no 02:00 run.
		{"NY hourly skipped hour: no run", "America/New_York", "0 * * * *", 10 * time.Minute,
			[]Result{Ping("backup", u(2026, 3, 8, 6, 1))}, u(2026, 3, 8, 7, 10), u(2026, 3, 8, 7, 10), nil},
		// Madrid spring 2026-03-29 02:00 CET -> 03:00 CEST; 02:30 runs at 03:00 CEST = 01:00Z.
		{"Madrid skipped: miss at jump", "Europe/Madrid", "30 2 * * *", 15 * time.Minute,
			[]Result{Ping("backup", u(2026, 3, 28, 1, 31))}, u(2026, 3, 29, 1, 16), u(2026, 3, 29, 1, 15),
			[]Result{miss(u(2026, 3, 29, 1, 15), "no ping for the 03:00 CEST run")}},
		{"Madrid skipped: ping counts", "Europe/Madrid", "30 2 * * *", 15 * time.Minute,
			[]Result{Ping("backup", u(2026, 3, 28, 1, 31)), Ping("backup", u(2026, 3, 29, 1, 2))}, u(2026, 3, 29, 12, 0), u(2026, 3, 30, 0, 45), nil},
		// Madrid fall 2026-10-25 03:00 CEST -> 02:00 CET; 02:30 runs once at 00:30Z.
		{"Madrid repeated: ping counts, no alert in second pass", "Europe/Madrid", "30 2 * * *", 15 * time.Minute,
			[]Result{Ping("backup", u(2026, 10, 24, 0, 31)), Ping("backup", u(2026, 10, 25, 0, 31))}, u(2026, 10, 25, 1, 50), u(2026, 10, 26, 1, 45), nil},
		{"Madrid repeated: miss not hidden", "Europe/Madrid", "30 2 * * *", 15 * time.Minute,
			[]Result{Ping("backup", u(2026, 10, 24, 0, 31))}, u(2026, 10, 25, 0, 46), u(2026, 10, 25, 0, 45),
			[]Result{miss(u(2026, 10, 25, 0, 45), "no ping for the 02:30 CEST run")}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			loc := zone(t, tt.zone)
			c := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: tt.cron, Grace: tt.grace}
			recent := newestFirst(tt.results...)
			if got := Deadline(c, recent, loc); !got.Equal(tt.deadline) {
				t.Fatalf("Deadline() = %v, want %v", got.UTC(), tt.deadline)
			}
			if got := Due(c, recent, tt.now, loc); !sameResults(got, tt.due) {
				t.Fatalf("Due() = %+v, want %+v", got, tt.due)
			}
		})
	}
}

func TestProbeCronEveryFiveMinutes(t *testing.T) {
	c := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "*/5 * * * *", Grace: 2 * time.Minute}
	at := func(m, s int) time.Time { return time.Date(2026, 11, 9, 10, m, s, 0, time.UTC) }
	recent := newestFirst(Ping("backup", at(0, 10)), Ping("backup", at(1, 0)), Ping("backup", at(2, 0)), Ping("backup", at(2, 29)))
	if got := RunResults(c, recent, nil); len(got) != 1 || !got[0].At.Equal(at(2, 29)) {
		t.Fatalf("RunResults() = %+v, want one cell", got)
	}
	if got := Deadline(c, recent, nil); !got.Equal(at(7, 0)) {
		t.Fatalf("Deadline() = %v", got)
	}
	if got := Due(c, recent, at(7, 1), nil); !sameResults(got, []Result{miss(at(7, 0), "no ping for the 10:05 UTC run")}) {
		t.Fatalf("Due() = %+v", got)
	}
	// L8: a ping nearer the coming run (10:03 for 10:05) counts for it.
	early := newestFirst(Ping("backup", at(0, 10)), Ping("backup", at(3, 0)))
	if got := Deadline(c, early, nil); !got.Equal(at(12, 0)) {
		t.Fatalf("Deadline(early) = %v", got)
	}
}

// Period heartbeats: Deadline/Due equal the baseline NextDeadline/Overdue for the same inputs.
func TestProbePeriodUnchanged(t *testing.T) {
	ba := zone(t, "America/Argentina/Buenos_Aires")
	c := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: 24 * time.Hour, Grace: 30 * time.Minute}
	t0 := time.Date(2026, 11, 9, 2, 5, 0, 0, ba)
	recents := [][]Result{
		newestFirst(Ping("backup", t0)),
		newestFirst(Ping("backup", t0), Fail("backup", t0.Add(time.Hour))),
		newestFirst(Ping("backup", t0), miss(t0.Add(24*time.Hour+30*time.Minute), "no ping since 02:05 -03")),
	}
	for i, r := range recents {
		if got, want := Deadline(c, r, ba), NextDeadline(c, r[0].At); !got.Equal(want) {
			t.Fatalf("%d: Deadline %v != NextDeadline %v", i, got, want)
		}
		var contact time.Time
		for _, x := range r {
			if !Missed(x) {
				contact = x.At
				break
			}
		}
		for _, now := range []time.Time{t0.Add(time.Hour), t0.Add(25 * time.Hour), t0.Add(80 * time.Hour)} {
			if got, want := Due(c, r, now, ba), Overdue(c, r[0].At, contact, now, ba); !sameResults(got, want) {
				t.Fatalf("%d %v: Due %+v != Overdue %+v", i, now, got, want)
			}
		}
		if got := RunResults(c, r, ba); !sameResults(got, r) {
			t.Fatalf("RunResults(period) changed results")
		}
	}
}
