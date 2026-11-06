package monitor

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/store"
)

func storedAttempts(t *testing.T, s *store.Store) []store.Attempt {
	t.Helper()
	got, err := s.Attempts(context.Background(), store.AttemptFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// brief is an attempt without its time, which follows the injected clock.
type brief struct {
	Event   store.Event
	Outcome store.Outcome
	Reason  string
}

func briefs(attempts []store.Attempt) []brief {
	var out []brief
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if a.Monitor != "api" || a.Channel != "mail" {
			out = append(out, brief{"wrong monitor or channel", store.OutcomeFailed, a.Monitor + "/" + a.Channel})
			continue
		}
		out = append(out, brief{a.Event, a.Outcome, a.Reason})
	}
	return out
}

func wantBriefs(t *testing.T, s *store.Store, want ...brief) {
	t.Helper()
	if got := briefs(storedAttempts(t, s)); !(len(got) == 0 && len(want) == 0) && !reflect.DeepEqual(got, want) {
		t.Fatalf("attempts (oldest first) = %+v, want %+v", got, want)
	}
}

func TestMaintenanceHoldsAreRecordedOncePerOwedAlert(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, up)
	r.feed(10, 30, down)
	r.feed(11, 0, down)
	r.feed(11, 30, down)
	r.want()
	wantBriefs(t, r.s, brief{store.EventDown, store.OutcomeHeld, "maintenance window"})
	if at := storedAttempts(t, r.s)[0].At; !at.Equal(time.Date(2030, 1, 7, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("held attempt time = %v, want the time the alert was first held", at)
	}
}

func TestRecoveryDuringHoldKeepsTheHeldRecordAndSendsNothing(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, up)
	r.feed(10, 30, down)
	r.feed(11, 0, up)
	r.feed(12, 30, up)
	r.want()
	wantBriefs(t, r.s, brief{store.EventDown, store.OutcomeHeld, "maintenance window"})
}

func TestHeldRecoveryIsRecordedAndSentAfterTheWindow(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, down)
	r.want(down)
	r.feed(10, 30, up)
	wantBriefs(t, r.s, brief{store.EventRecovered, store.OutcomeHeld, "maintenance window"})
	r.feed(12, 30, up)
	// Today's semantics: the recovery goes out once the window is over. The
	// fake alerter does not record sent attempts; the notifier does.
	r.want(down, up)
	wantBriefs(t, r.s, brief{store.EventRecovered, store.OutcomeHeld, "maintenance window"})
}

func TestMuteHoldsAreRecordedWithTheMuteReason(t *testing.T) {
	s := openStore(t)
	alertingCheck(t, s, 1, 1)
	a := &recordingAlerter{}
	D, U := check.StatusDown, check.StatusUp
	if err := s.SetMute(context.Background(), "api", time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	feed(t, s, a, 0, D, D, U)
	if got := a.snapshot(); len(got) != 0 {
		t.Fatalf("muted alerts: %v", got)
	}
	wantBriefs(t, s, brief{store.EventDown, store.OutcomeHeld, "muted"})
	// A new outage under the same mute is a new owed alert.
	feed(t, s, a, 3, D)
	wantBriefs(t, s, brief{store.EventDown, store.OutcomeHeld, "muted"}, brief{store.EventDown, store.OutcomeHeld, "muted"})
}

func TestNothingHeldWithoutASuppression(t *testing.T) {
	s := openStore(t)
	alertingCheck(t, s, 1, 1)
	a := &recordingAlerter{}
	feed(t, s, a, 0, check.StatusDown, check.StatusUp)
	if got := a.snapshot(); len(got) != 2 {
		t.Fatalf("alerts: %v", got)
	}
	wantBriefs(t, s)
}

func TestCertWarningHeldByMaintenanceIsRecordedOnce(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	now := time.Date(2030, 1, 7, 10, 30, 0, 0, time.UTC)
	r.runner.cert = &check.CertInfo{Host: "host", Issuer: "ca", Serial: "a", NotAfter: now.Add(48 * time.Hour)}
	r.start()
	r.feed(10, 30, up)
	r.feed(10, 40, up)
	if got := r.alert.got(); len(got) != 0 {
		t.Fatalf("cert warnings during the window: %v", got)
	}
	wantBriefs(t, r.s, brief{store.EventCert, store.OutcomeHeld, "maintenance window"})
}

func TestCertWarningWithoutChannelOrSuppressionRecordsNothingHere(t *testing.T) {
	r := newRig(t)
	now := time.Date(2030, 1, 7, 9, 0, 0, 0, time.UTC)
	r.runner.cert = &check.CertInfo{Host: "host", Issuer: "ca", Serial: "a", NotAfter: now.Add(48 * time.Hour)}
	r.start()
	r.feed(9, 30, up)
	if got := r.alert.got(); len(got) != 1 {
		t.Fatalf("cert warnings: %v", got)
	}
	// Sent attempts are the notifier's to record, not the monitor's.
	wantBriefs(t, r.s)
}
