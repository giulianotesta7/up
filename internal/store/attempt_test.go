package store_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/store"
)

func attempt(monitor, channel string, minute int, outcome store.Outcome) store.Attempt {
	return store.Attempt{At: at.Add(time.Duration(minute) * time.Minute), Monitor: monitor, Channel: channel, Event: store.EventDown, Outcome: outcome, Reason: "status 502"}
}

// attemptStore is a store with monitors api and web and channels ops and pager.
func attemptStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	s, path := seeded(t)
	if err := s.AddSMTPServer(ctx, smtp("s")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ops", "pager"} {
		if err := s.AddChannel(ctx, channel(name)); err != nil {
			t.Fatal(err)
		}
	}
	return s, path
}

func mustRecord(t *testing.T, s *store.Store, attempts ...store.Attempt) {
	t.Helper()
	for _, a := range attempts {
		if err := s.RecordAttempt(ctx, a); err != nil {
			t.Fatalf("RecordAttempt(%+v) = %v", a, err)
		}
	}
}

func TestAttemptsRoundTripNewestFirstAndBounded(t *testing.T) {
	s, path := attemptStore(t)
	a1 := attempt("api", "ops", 1, store.OutcomeFailed)
	a2 := attempt("web", "pager", 2, store.OutcomeSent)
	a2.Event, a2.Reason = store.EventRecovered, ""
	a3 := attempt("api", "ops", 3, store.OutcomeHeld)
	a3.Event, a3.Reason = store.EventCert, "maintenance window"
	mustRecord(t, s, a1, a2, a3)

	got, err := s.Attempts(ctx, store.AttemptFilter{})
	if err != nil || !reflect.DeepEqual(got, []store.Attempt{a3, a2, a1}) {
		t.Fatalf("Attempts() = %+v, %v, want newest first %+v", got, err, []store.Attempt{a3, a2, a1})
	}
	if got, err := s.Attempts(ctx, store.AttemptFilter{Limit: 2}); err != nil || !reflect.DeepEqual(got, []store.Attempt{a3, a2}) {
		t.Fatalf("Attempts(limit 2) = %+v, %v", got, err)
	}
	if got, err := s.Attempts(ctx, store.AttemptFilter{Channel: "ops"}); err != nil || !reflect.DeepEqual(got, []store.Attempt{a3, a1}) {
		t.Fatalf("Attempts(channel ops) = %+v, %v", got, err)
	}
	if got, err := s.Attempts(ctx, store.AttemptFilter{Monitor: "web"}); err != nil || !reflect.DeepEqual(got, []store.Attempt{a2}) {
		t.Fatalf("Attempts(monitor web) = %+v, %v", got, err)
	}
	if got, err := s.Attempts(ctx, store.AttemptFilter{Monitor: "api", Channel: "pager"}); err != nil || len(got) != 0 {
		t.Fatalf("Attempts(api+pager) = %+v, %v, want none", got, err)
	}
	s.Close()
	again, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, err := again.Attempts(ctx, store.AttemptFilter{}); err != nil || len(got) != 3 {
		t.Fatalf("after reopen = %+v, %v", got, err)
	}
}

func TestAttemptsLimitDefaultsAndCaps(t *testing.T) {
	s, _ := attemptStore(t)
	for i := 0; i < store.MaxAttemptLimit+5; i++ {
		mustRecord(t, s, attempt("api", "ops", i, store.OutcomeSent))
	}
	if got, _ := s.Attempts(ctx, store.AttemptFilter{}); len(got) != store.DefaultAttemptLimit {
		t.Fatalf("default limit returned %d, want %d", len(got), store.DefaultAttemptLimit)
	}
	if got, _ := s.Attempts(ctx, store.AttemptFilter{Limit: 100000}); len(got) != store.MaxAttemptLimit {
		t.Fatalf("huge limit returned %d, want %d", len(got), store.MaxAttemptLimit)
	}
}

func TestLastAttemptsIsNewestPerChannel(t *testing.T) {
	s, _ := attemptStore(t)
	if got, err := s.LastAttempts(ctx); err != nil || len(got) != 0 {
		t.Fatalf("LastAttempts() empty = %+v, %v", got, err)
	}
	old := attempt("api", "ops", 1, store.OutcomeSent)
	newest := attempt("web", "ops", 5, store.OutcomeFailed)
	other := attempt("api", "pager", 3, store.OutcomeHeld)
	mustRecord(t, s, old, newest, other)
	got, err := s.LastAttempts(ctx)
	if err != nil || !reflect.DeepEqual(got, []store.Attempt{newest, other}) {
		t.Fatalf("LastAttempts() = %+v, %v, want one per channel ordered by channel", got, err)
	}
}

func TestRecordAttemptRejectsInvalidInputAndStoresNothing(t *testing.T) {
	s, _ := attemptStore(t)
	good := attempt("api", "ops", 1, store.OutcomeSent)
	cases := map[string]func(*store.Attempt){
		"event":   func(a *store.Attempt) { a.Event = "channel.test" },
		"outcome": func(a *store.Attempt) { a.Outcome = "pending" },
		"monitor": func(a *store.Attempt) { a.Monitor = "" },
		"channel": func(a *store.Attempt) { a.Channel = "" },
	}
	for name, mutate := range cases {
		a := good
		mutate(&a)
		if err := s.RecordAttempt(ctx, a); err == nil {
			t.Errorf("%s: RecordAttempt accepted %+v", name, a)
		}
	}
	// A monitor that does not exist (for example deleted while the alert was in
	// flight) records nothing and is ErrNotFound.
	gone := good
	gone.Monitor = "ghost"
	if err := s.RecordAttempt(ctx, gone); err == nil || !strings.Contains(err.Error(), `monitor "ghost" not found`) {
		t.Errorf("unknown monitor error = %v", err)
	}
	if got, _ := s.Attempts(ctx, store.AttemptFilter{}); len(got) != 0 {
		t.Fatalf("rejected attempts stored %+v", got)
	}
}

func TestRecordAttemptCleansReason(t *testing.T) {
	s, _ := attemptStore(t)
	a := attempt("api", "ops", 1, store.OutcomeFailed)
	a.Reason = "line one\r\nline\ttwo \x00" + strings.Repeat("x", 400)
	mustRecord(t, s, a)
	got, _ := s.Attempts(ctx, store.AttemptFilter{})
	if len(got) != 1 || strings.ContainsAny(got[0].Reason, "\r\n\t\x00") || !strings.HasPrefix(got[0].Reason, "line one line two x") || len([]rune(got[0].Reason)) != store.MaxAttemptReason {
		t.Fatalf("reason = %q (%d runes)", got[0].Reason, len([]rune(got[0].Reason)))
	}
}

func TestPruneDeletesOldAttemptsWithTheCheckRetention(t *testing.T) {
	s, _ := attemptStore(t)
	old := attempt("api", "ops", -120, store.OutcomeSent)
	fresh := attempt("api", "ops", 10, store.OutcomeFailed)
	mustRecord(t, s, old, fresh)
	before, _ := s.Recent(ctx, "api", 100)
	deleted, err := s.Prune(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.Attempts(ctx, store.AttemptFilter{})
	if !reflect.DeepEqual(got, []store.Attempt{fresh}) {
		t.Fatalf("attempts after prune = %+v, want only the fresh one", got)
	}
	// The count reports results only, as before, and results prune as before.
	after, _ := s.Recent(ctx, "api", 100)
	if int(deleted) != len(before)-len(after) {
		t.Fatalf("Prune() = %d, results %d -> %d", deleted, len(before), len(after))
	}
}

func TestDeleteCheckRemovesItsAttemptsAndKeepsOthers(t *testing.T) {
	s, _ := attemptStore(t)
	web := attempt("web", "ops", 2, store.OutcomeSent)
	mustRecord(t, s, attempt("api", "ops", 1, store.OutcomeFailed), web)
	if err := s.DeleteCheck(ctx, "api"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Attempts(ctx, store.AttemptFilter{}); !reflect.DeepEqual(got, []store.Attempt{web}) {
		t.Fatalf("attempts after deleting api = %+v", got)
	}
}

func TestDeleteChannelKeepsItsAttempts(t *testing.T) {
	s, _ := attemptStore(t)
	a := attempt("api", "pager", 1, store.OutcomeSent)
	mustRecord(t, s, a)
	if err := s.DeleteChannel(ctx, "pager"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Attempts(ctx, store.AttemptFilter{Channel: "pager"}); !reflect.DeepEqual(got, []store.Attempt{a}) {
		t.Fatalf("attempts after deleting the channel = %+v, want the history kept", got)
	}
}

func TestRecordAttemptLeavesChecksAndResultsAlone(t *testing.T) {
	s, _ := attemptStore(t)
	before := observe(t, s)
	mustRecord(t, s, attempt("api", "ops", 1, store.OutcomeFailed))
	assertUnchanged(t, s, before)
	if err := s.Record(ctx, check.Result{CheckName: "api", At: at.Add(time.Hour), Status: check.StatusUp}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Attempts(ctx, store.AttemptFilter{}); len(got) != 1 {
		t.Fatalf("recording a result changed attempts: %+v", got)
	}
}
