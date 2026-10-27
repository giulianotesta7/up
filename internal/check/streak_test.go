package check

import "testing"

func observeAll(s *Streak, alertAfter, recoverAfter int, statuses ...Status) []Transition {
	var out []Transition
	for _, st := range statuses {
		out = append(out, s.Observe(st, alertAfter, recoverAfter))
	}
	return out
}

func TestStreakAlertsOnlyAfterConsecutiveFailures(t *testing.T) {
	var s Streak
	got := observeAll(&s, 3, 1, StatusDown, StatusDown, StatusDown, StatusDown)
	want := []Transition{NoChange, NoChange, WentDown, NoChange}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("result %d: got %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
	if !s.Alerting {
		t.Fatal("Alerting = false after three failures")
	}
}

func TestStreakIsolatedFailureNeverAlerts(t *testing.T) {
	var s Streak
	for _, tr := range observeAll(&s, 3, 1, StatusDown, StatusUp, StatusDown, StatusUp, StatusDown, StatusDown, StatusUp) {
		if tr != NoChange {
			t.Fatalf("unexpected transition %v", tr)
		}
	}
	if s.Alerting || s.Failures != 0 {
		t.Fatalf("state = %+v, want idle", s)
	}
}

func TestStreakRecoversAfterConsecutiveSuccesses(t *testing.T) {
	var s Streak
	got := observeAll(&s, 1, 2, StatusDown, StatusUp, StatusDown, StatusUp, StatusUp)
	want := []Transition{WentDown, NoChange, NoChange, NoChange, Recovered}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("result %d: got %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
}

func TestStreakDefaultsKeepPreviousBehavior(t *testing.T) {
	var s Streak
	got := observeAll(&s, 0, 0, StatusDown, StatusDown, StatusUp, StatusUp)
	want := []Transition{WentDown, NoChange, Recovered, NoChange}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("result %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestReplayRebuildsStateFromNewestFirstHistory(t *testing.T) {
	history := []Result{{Status: StatusDown}, {Status: StatusDown}, {Status: StatusUp}} // newest first
	s := Replay(history, 3, 1)
	if s.Alerting || s.Failures != 2 {
		t.Fatalf("state = %+v, want 2 failures not yet alerting", s)
	}
	s = Replay([]Result{{Status: StatusDown}, {Status: StatusDown}, {Status: StatusDown}}, 3, 1)
	if !s.Alerting {
		t.Fatalf("state = %+v, want alerting", s)
	}
}
