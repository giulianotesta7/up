package tui

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/heohlee/up/internal/store"
)

func TestLoadCarriesBoundedDeliveryAttempts(t *testing.T) {
	ctx := context.Background()
	w, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.SetChecks(ctx, testChecks); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	total := AttemptLimit + 30
	var newest store.Attempt
	for i := 0; i < total; i++ {
		a := store.Attempt{At: base.Add(time.Duration(i) * time.Minute), Monitor: "api", Channel: "ops", Event: store.EventDown, Outcome: store.OutcomeSent}
		if i%2 == 1 {
			a.Monitor, a.Channel, a.Outcome, a.Reason = "payments", "pager", store.OutcomeFailed, "status 502"
		}
		if err := w.RecordAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
		newest = a
	}
	snap, err := Load(ctx, w, base.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Attempts) != AttemptLimit || !reflect.DeepEqual(snap.Attempts[0], newest) || !snap.Attempts[0].At.After(snap.Attempts[1].At) {
		t.Fatalf("Attempts = %d entries, first %+v; want the newest %d, newest first", len(snap.Attempts), snap.Attempts[0], AttemptLimit)
	}
	if len(snap.LastAttempts) != 2 || snap.LastAttempts[0].Channel != "ops" || snap.LastAttempts[1].Channel != "pager" || !snap.LastAttempts[1].At.Equal(newest.At) {
		t.Fatalf("LastAttempts = %+v, want the newest of ops then pager", snap.LastAttempts)
	}
	api, pay, fresh := snap.Services[0], snap.Services[1], snap.Services[2]
	if len(api.Attempts) != ServiceAttemptLimit || len(pay.Attempts) != ServiceAttemptLimit || len(fresh.Attempts) != 0 {
		t.Fatalf("service attempts = %d, %d, %d", len(api.Attempts), len(pay.Attempts), len(fresh.Attempts))
	}
	for _, a := range pay.Attempts {
		if a.Monitor != "payments" {
			t.Fatalf("payments carries %+v", a)
		}
	}
	if !pay.Attempts[0].At.Equal(newest.At) || !api.Attempts[0].At.Equal(newest.At.Add(-time.Minute)) {
		t.Fatalf("not newest first: %+v / %+v", pay.Attempts[0], api.Attempts[0])
	}
}

// A store that cannot read attempts (and an empty log) leave them empty.
func TestLoadWithoutAttemptsLeavesThemEmpty(t *testing.T) {
	snap := seededSnapshot(t, now)
	if snap.Attempts != nil || snap.LastAttempts != nil || snap.Services[0].Attempts != nil {
		t.Fatalf("attempts = %+v %+v %+v, want none", snap.Attempts, snap.LastAttempts, snap.Services[0].Attempts)
	}
}
