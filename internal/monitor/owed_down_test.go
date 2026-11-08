package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
)

// setChannel edits the "api" check's channel and waits for the daemon to
// restart its loop on the new definition.
func (r *maintenanceRig) setChannel(channel string) {
	r.t.Helper()
	c, err := r.s.Checks(context.Background())
	if err != nil || len(c) != 1 {
		r.t.Fatalf("checks = %v, %v", c, err)
	}
	c[0].Channel = channel
	before := r.runner.entered.Load()
	if err := r.s.UpdateCheck(context.Background(), c[0]); err != nil {
		r.t.Fatal(err)
	}
	waitFor(r.t, "the restarted loop", func() bool { return r.runner.entered.Load() > before })
}

func (r *maintenanceRig) waitAlerts(want ...check.Status) {
	r.t.Helper()
	waitFor(r.t, "the owed alert", func() bool { return len(r.alerts()) >= len(want) })
	r.want(want...)
}

func (r *maintenanceRig) quiet() {
	r.t.Helper()
	time.Sleep(150 * time.Millisecond)
}

func TestOwedDownIsSentWhenAChannelIsAttached(t *testing.T) {
	r := newRig(t)
	r.setChannelBeforeStart("")
	r.start()
	r.feed(9, 10, down)
	r.want()
	r.setChannel("mail")
	r.waitAlerts(down)
	r.feed(9, 20, up)
	r.want(down, up)
}

func TestOwedDownWaitsForAMuteThenIsSent(t *testing.T) {
	r := newRig(t)
	r.setChannelBeforeStart("")
	r.slowChecks()
	r.start()
	r.feed(9, 10, down)
	if err := r.s.SetMute(context.Background(), "api", time.Date(2030, 1, 7, 9, 30, 0, 0, time.UTC), false); err != nil {
		t.Fatal(err)
	}
	r.setChannel("mail")
	r.feed(9, 20, down)
	r.quiet()
	r.want()
	r.clock.set(9, 40)
	r.waitAlerts(down)
	r.quiet()
	r.want(down)
}

func TestOwedDownWaitsForMaintenanceThenIsSentOnce(t *testing.T) {
	r := newRig(t)
	r.setChannelBeforeStart("")
	r.addWindow(10, 2)
	r.slowChecks()
	r.start()
	r.feed(10, 30, down)
	r.setChannel("mail")
	r.feed(10, 40, down)
	r.quiet()
	r.want()
	r.clock.set(12, 30)
	r.waitAlerts(down)
	r.quiet()
	r.want(down)
}

func TestAttachingAChannelToAHealthyMonitorSendsNothing(t *testing.T) {
	r := newRig(t)
	r.setChannelBeforeStart("")
	r.start()
	r.feed(9, 10, up)
	r.setChannel("mail")
	r.quiet()
	r.want()
}

func TestOwedDownIsNotResentWhenTheChannelIsRemovedAndReattached(t *testing.T) {
	r := newRig(t)
	r.start()
	r.feed(9, 10, down)
	r.want(down)
	r.setChannel("")
	r.setChannel("mail")
	r.quiet()
	r.want(down)
	r.feed(9, 20, up)
	r.want(down, up)
}

func TestOwedDownIsNotDuplicatedByARestartAfterAttach(t *testing.T) {
	r := newRig(t)
	r.setChannelBeforeStart("")
	r.start()
	r.feed(9, 10, down)
	r.setChannel("mail")
	r.waitAlerts(down)
	r.stop()
	r.start()
	r.feed(9, 20, down)
	r.quiet()
	r.want(down)
}

func TestKnowledgeIsNotLearnedWhileTheCheckHasNoChannel(t *testing.T) {
	r := newRig(t)
	r.setChannelBeforeStart("")
	r.start()
	r.feed(9, 10, down)
	known, found, err := r.s.AlertState(context.Background(), "api")
	if err != nil || known || !found {
		t.Fatalf("alert state = %v, %v, %v; want recorded false", known, found, err)
	}
}

func (r *maintenanceRig) setChannelBeforeStart(channel string) {
	r.t.Helper()
	c, err := r.s.Checks(context.Background())
	if err != nil || len(c) != 1 {
		r.t.Fatalf("checks = %v, %v", c, err)
	}
	c[0].Channel = channel
	if err := r.s.UpdateCheck(context.Background(), c[0]); err != nil {
		r.t.Fatal(err)
	}
}
