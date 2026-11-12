package monitor

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/store"
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

// restart stops the daemon and starts it again on the same database.
func (r *maintenanceRig) restart() {
	r.t.Helper()
	r.stop()
	r.start()
}

// editDefinition changes the check's timeout, which restarts its loop.
func (r *maintenanceRig) editDefinition() {
	r.t.Helper()
	c, err := r.s.Checks(context.Background())
	if err != nil || len(c) != 1 {
		r.t.Fatalf("checks = %v, %v", c, err)
	}
	c[0].Timeout += time.Millisecond
	before := r.runner.entered.Load()
	if err := r.s.UpdateCheck(context.Background(), c[0]); err != nil {
		r.t.Fatal(err)
	}
	waitFor(r.t, "the restarted loop", func() bool { return r.runner.entered.Load() > before })
}

// sent records a sent attempt as the notifier does after a delivery.
func (r *maintenanceRig) sent(event store.Event) {
	r.t.Helper()
	if err := r.s.RecordAttempt(context.Background(), store.Attempt{At: r.clock.Now(), Monitor: "api", Channel: "mail", Event: event, Outcome: store.OutcomeSent}); err != nil {
		r.t.Fatal(err)
	}
}

func TestMuteHoldIsRecordedOnceAcrossDaemonRestarts(t *testing.T) {
	s := openStore(t)
	alertingCheck(t, s, 1, 1)
	a := &recordingAlerter{}
	D := check.StatusDown
	if err := s.SetMute(context.Background(), "api", time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	feed(t, s, a, 0, D)
	feed(t, s, a, 1, D)
	feed(t, s, a, 2, D)
	if got := a.snapshot(); len(got) != 0 {
		t.Fatalf("muted alerts: %v", got)
	}
	wantBriefs(t, s, brief{store.EventDown, store.OutcomeHeld, "muted"})
}

func TestHeldDownIsRecordedOnceAcrossRestartAndEdit(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(10, 30, down)
	r.restart()
	r.feed(10, 40, down)
	r.editDefinition()
	r.feed(10, 50, down)
	r.want()
	held := brief{store.EventDown, store.OutcomeHeld, "maintenance window"}
	wantBriefs(t, r.s, held)

	// The window ends: the down alert goes out and the notifier logs it.
	r.feed(12, 30, down)
	r.want(down)
	r.sent(store.EventDown)
	r.feed(12, 40, up)
	r.want(down, up)
	r.sent(store.EventRecovered)
	sentDown, sentUp := brief{store.EventDown, store.OutcomeSent, ""}, brief{store.EventRecovered, store.OutcomeSent, ""}

	// A new outage in the next day's window is a new owed alert, even after a
	// restart, and is still logged once.
	r.restart()
	r.feedAt(time.Date(2030, 1, 8, 10, 30, 0, 0, time.UTC), down)
	r.restart()
	r.feedAt(time.Date(2030, 1, 8, 10, 40, 0, 0, time.UTC), down)
	wantBriefs(t, r.s, held, sentDown, sentUp, held)
}

// A hold that ended without a delivery (the monitor recovered during the
// window) does not hide the next outage's hold after a restart.
func TestHoldEndedByRecoveryDoesNotHideTheNextHoldAfterRestart(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(10, 30, down)
	r.feed(10, 40, up)
	r.restart()
	r.feed(10, 50, down)
	r.want()
	held := brief{store.EventDown, store.OutcomeHeld, "maintenance window"}
	wantBriefs(t, r.s, held, held)
}

func TestHeldCertWarningIsRecordedOncePerThresholdAcrossRestartAndEdit(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	first := time.Date(2030, 1, 7, 10, 30, 0, 0, time.UTC)
	r.runner.cert = &check.CertInfo{Host: "host", Issuer: "ca", Serial: "a", NotAfter: first.Add(10 * 24 * time.Hour)}
	r.start()
	r.feed(10, 30, up)
	r.restart()
	r.feed(10, 40, up)
	r.editDefinition()
	r.feed(10, 50, up)
	if got := r.alert.got(); len(got) != 0 {
		t.Fatalf("cert warnings during the window: %v", got)
	}
	held := brief{store.EventCert, store.OutcomeHeld, "maintenance window"}
	wantBriefs(t, r.s, held)

	// The window ends: the 14-day warning goes out and the notifier logs it.
	r.feed(12, 30, up)
	if got := r.alert.got(); !reflect.DeepEqual(got, []int{14}) {
		t.Fatalf("cert warnings after the window: %v", got)
	}
	r.sent(store.EventCert)

	// The 7-day threshold falls due in a later window: a new hold, logged once.
	r.restart()
	r.feedAt(first.Add(4*24*time.Hour), up)
	r.restart()
	r.feedAt(first.Add(4*24*time.Hour+10*time.Minute), up)
	wantBriefs(t, r.s, held, brief{store.EventCert, store.OutcomeSent, ""}, held)
	if got := r.alert.got(); !reflect.DeepEqual(got, []int{14}) {
		t.Fatalf("cert warnings: %v", got)
	}
}

// setThresholds changes the check's alert-after and recover-after; when the
// daemon runs, it waits for the restarted loop.
func (r *maintenanceRig) setThresholds(alertAfter, recoverAfter int) {
	r.t.Helper()
	c, err := r.s.Checks(context.Background())
	if err != nil || len(c) != 1 {
		r.t.Fatalf("checks = %v, %v", c, err)
	}
	c[0].AlertAfter, c[0].RecoverAfter = alertAfter, recoverAfter
	before := r.runner.entered.Load()
	if err := r.s.UpdateCheck(context.Background(), c[0]); err != nil {
		r.t.Fatal(err)
	}
	if r.cancel != nil {
		waitFor(r.t, "the restarted loop", func() bool { return r.runner.entered.Load() > before })
	}
}

// An edit that makes a new outage's down alert owed when the loop restarts
// logs a new hold: the earlier hold ended when the monitor recovered.
func TestHeldDownAfterRecoveryIsRecordedWhenAlertAfterIsEdited(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.setThresholds(2, 1)
	r.start()
	r.feed(10, 10, down)
	r.feed(10, 20, down)
	r.feed(10, 30, up)
	r.feed(10, 40, down)
	r.setThresholds(1, 1)
	r.feed(10, 50, down)
	r.want()
	held := brief{store.EventDown, store.OutcomeHeld, "maintenance window"}
	wantBriefs(t, r.s, held, held)
}

// The same for a recovery: an outage between the two recoveries ended the
// earlier hold.
func TestHeldRecoveryAfterNewOutageIsRecordedWhenRecoverAfterIsEdited(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.setThresholds(1, 2)
	r.start()
	r.feed(9, 30, down)
	r.want(down)
	r.sent(store.EventDown)
	r.feed(10, 10, up)
	r.feed(10, 20, up)
	r.feed(10, 30, down)
	r.feed(10, 40, up)
	r.setThresholds(1, 1)
	r.feed(10, 50, up)
	r.want(down)
	held := brief{store.EventRecovered, store.OutcomeHeld, "maintenance window"}
	wantBriefs(t, r.s, brief{store.EventDown, store.OutcomeSent, ""}, held, held)
}
