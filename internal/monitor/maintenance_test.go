package monitor

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(h, m int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = time.Date(2030, 1, 7, h, m, 0, 0, time.UTC)
}

// clockRunner reports statuses fed by the test, stamped with the fake clock.
type clockRunner struct {
	values chan check.Status
	clock  *fakeClock
	cert   *check.CertInfo
}

func (r *clockRunner) Run(ctx context.Context, c config.Check) check.Result {
	select {
	case status := <-r.values:
		return check.Result{CheckName: c.Name, At: r.clock.Now(), Status: status, Cert: r.cert}
	case <-ctx.Done():
		return check.Result{Status: check.StatusCanceled}
	}
}

type maintenanceRig struct {
	t      *testing.T
	s      *store.Store
	clock  *fakeClock
	runner *clockRunner
	alert  *certRecorder
	cancel context.CancelFunc
	done   chan error
	count  int
}

// newRig has a daemon with an alerting "api" check. The window is every day
// from 10:00 for two hours (UTC); the fake clock starts at 09:00.
func newRig(t *testing.T) *maintenanceRig {
	t.Helper()
	s := openStore(t)
	alertingCheck(t, s, 1, 1)
	clock := &fakeClock{}
	clock.set(9, 0)
	r := &maintenanceRig{t: t, s: s, clock: clock, runner: &clockRunner{values: make(chan check.Status, 20), clock: clock}, alert: &certRecorder{}}
	return r
}

func (r *maintenanceRig) addWindow(startHour int, hours int) config.MaintenanceWindow {
	r.t.Helper()
	w, err := r.s.AddMaintenanceWindow(context.Background(), config.MaintenanceWindow{
		Check: "api", Weekdays: []time.Weekday{0, 1, 2, 3, 4, 5, 6}, StartMinute: startHour * 60, Duration: time.Duration(hours) * time.Hour, Zone: "UTC",
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return w
}

func (r *maintenanceRig) start() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan error, 1)
	go func() {
		r.done <- Run(ctx, Options{Store: r.s, Runner: r.runner, Alerter: r.alert, Now: r.clock.Now, HeartbeatInterval: time.Hour, DiscoveryInterval: 20 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	r.t.Cleanup(r.stop)
}

func (r *maintenanceRig) stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
		r.cancel = nil
	}
}

// feed gives the monitor one result at the given fake time and waits for it
// to be recorded.
func (r *maintenanceRig) feed(h, m int, status check.Status) {
	r.t.Helper()
	r.clock.set(h, m)
	r.runner.values <- status
	r.count++
	waitFor(r.t, "recorded result", func() bool {
		rows, _ := r.s.Recent(context.Background(), "api", 100)
		return len(rows) >= r.count
	})
	// The alert, if any, is sent after the result is stored; let the loop
	// finish its step by waiting for it to ask for the next result.
	waitFor(r.t, "loop idle", func() bool { return len(r.runner.values) == 0 })
	time.Sleep(30 * time.Millisecond)
}

// slowChecks makes the check run once an hour, so only the monitor's own
// recheck can notice that suppression ended.
func (r *maintenanceRig) slowChecks() {
	r.t.Helper()
	c, err := r.s.Checks(context.Background())
	if err != nil || len(c) != 1 {
		r.t.Fatalf("checks = %v, %v", c, err)
	}
	c[0].Interval = time.Hour
	if err := r.s.UpdateCheck(context.Background(), c[0]); err != nil {
		r.t.Fatal(err)
	}
}

func (r *maintenanceRig) alerts() []check.Status { return r.alert.snapshot() }

func (r *maintenanceRig) want(want ...check.Status) {
	r.t.Helper()
	if got := r.alerts(); !(len(got) == 0 && len(want) == 0) && !reflect.DeepEqual(got, want) {
		r.t.Fatalf("alerts = %v, want %v", got, want)
	}
}

var down, up = check.StatusDown, check.StatusUp

func TestMaintenanceDownDuringWindowSendsNothing(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, up)
	r.feed(10, 30, down)
	r.feed(11, 0, down)
	r.want()
	// Checks and history keep running and counting.
	rows, _ := r.s.Recent(context.Background(), "api", 100)
	if len(rows) != 3 || rows[0].Status != down {
		t.Fatalf("history = %+v", rows)
	}
}

func TestMaintenanceStillDownAfterWindowSendsOneDownAlert(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, up)
	r.feed(10, 30, down)
	r.feed(12, 30, down)
	r.feed(12, 40, down)
	r.want(down)
}

func TestMaintenanceOutageInsideWindowSendsNothing(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, up)
	r.feed(10, 30, down)
	r.feed(11, 0, up)
	r.feed(12, 30, up)
	r.want()
}

func TestMaintenanceRecoveryInsideWindowIsHeldUntilEnd(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, down)
	r.want(down)
	r.feed(10, 30, up)
	r.want(down)
	r.feed(12, 30, up)
	r.want(down, up)
}

func TestMaintenanceAndMuteReconcileOnceAtTheLaterEnd(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	// The mute outlasts the window.
	if err := r.s.SetMute(context.Background(), "api", time.Date(2030, 1, 7, 13, 0, 0, 0, time.UTC), false); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.feed(9, 30, down)
	r.want()
	r.feed(10, 30, down)
	r.feed(12, 30, down) // window over, mute still on
	r.want()
	r.feed(13, 30, down) // all suppression over
	r.want(down)
	r.feed(13, 40, down)
	r.want(down)
	if _, ok, _ := r.s.Mute(context.Background(), "api"); ok {
		t.Fatal("mute not cleared after reconciling")
	}
}

func TestMaintenanceOutlastingMuteWaitsForTheWindow(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 4)
	if err := r.s.SetMute(context.Background(), "api", time.Date(2030, 1, 7, 11, 0, 0, 0, time.UTC), false); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.feed(10, 30, down)
	r.feed(11, 30, down) // mute over, window still on
	r.want()
	r.feed(14, 30, down)
	r.want(down)
}

func TestMaintenanceBackToBackWindowsDoNotReconcileBetween(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.addWindow(12, 2)
	r.start()
	r.feed(10, 30, down)
	r.feed(12, 0, down) // the boundary: still suppressed by the next window
	r.feed(13, 0, down)
	r.want()
	r.feed(14, 30, down)
	r.want(down)
}

func TestMaintenanceSuppressionEndsWithoutAnotherResult(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.slowChecks()
	r.start()
	r.feed(10, 30, down)
	r.want()
	r.clock.set(12, 30)
	waitFor(t, "the alert after the window", func() bool { return len(r.alerts()) == 1 })
}

func TestMaintenanceWindowDeletedWhileRunningEndsSuppression(t *testing.T) {
	r := newRig(t)
	w := r.addWindow(10, 2)
	r.slowChecks()
	r.start()
	r.feed(10, 30, down)
	r.want()
	if err := r.s.DeleteMaintenanceWindow(context.Background(), "api", w.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the alert after deleting the window", func() bool { return len(r.alerts()) == 1 })
}

func TestMaintenanceSurvivesRestart(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, up)
	r.feed(10, 30, down)
	r.stop()
	r.start() // a fresh daemon in the middle of the window
	r.feed(11, 0, down)
	r.want()
	r.feed(12, 30, down)
	r.want(down)
}

func TestMaintenanceDelaysCertWarning(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	now := time.Date(2030, 1, 7, 0, 0, 0, 0, time.UTC)
	r.runner.cert = &check.CertInfo{Host: "example.com", Issuer: "ca", Serial: "1", NotAfter: now.Add(48 * time.Hour)}
	r.start()
	r.feed(10, 30, up)
	if got := r.alert.got(); len(got) != 0 {
		t.Fatalf("cert warnings during window: %v", got)
	}
	r.feed(12, 30, up)
	if got := r.alert.got(); !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("cert warnings after window: %v, want [3]", got)
	}
}

// PRESERVE: without windows a manual mute holds alerts and reconciles once.
func TestMaintenanceMuteWithoutWindowsBehavesAsBefore(t *testing.T) {
	r := newRig(t)
	if err := r.s.SetMute(context.Background(), "api", time.Date(2030, 1, 7, 11, 0, 0, 0, time.UTC), false); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.feed(9, 30, down)
	r.feed(10, 30, down)
	r.want()
	r.feed(11, 30, down)
	r.feed(11, 40, down)
	r.want(down)
	if _, ok, _ := r.s.Mute(context.Background(), "api"); ok {
		t.Fatal("mute not cleared after reconciling")
	}
}
