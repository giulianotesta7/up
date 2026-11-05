package monitor

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
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

func (c *fakeClock) set(h, m int) { c.setAt(time.Date(2030, 1, 7, h, m, 0, 0, time.UTC)) }

func (c *fakeClock) setAt(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// clockRunner reports statuses fed by the test, stamped with the fake clock.
type clockRunner struct {
	values chan check.Status
	clock  *fakeClock
	cert   *check.CertInfo
	// entered counts Run calls: a loop calls Run only after reading its
	// initial alert state.
	entered atomic.Int64
}

func (r *clockRunner) Run(ctx context.Context, c config.Check) check.Result {
	r.entered.Add(1)
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
	before := r.runner.entered.Load()
	go func() {
		r.done <- Run(ctx, Options{Store: r.s, Runner: r.runner, Alerter: r.alert, Now: r.clock.Now, HeartbeatInterval: time.Hour, DiscoveryInterval: 20 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	r.t.Cleanup(r.stop)
	// Do not move the injected clock before the loop read its initial state.
	waitFor(r.t, "the loop to start", func() bool { return r.runner.entered.Load() > before })
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
	r.feedAt(time.Date(2030, 1, 7, h, m, 0, 0, time.UTC), status)
}

func (r *maintenanceRig) feedAt(at time.Time, status check.Status) {
	r.t.Helper()
	r.clock.setAt(at)
	r.runner.values <- status
	r.count++
	waitFor(r.t, "recorded result", func() bool {
		rows, _ := r.s.Recent(context.Background(), "api", 1000)
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

// record stores n results of one status a second apart, ending at last, as if
// an earlier daemon had run through them.
func (r *maintenanceRig) record(n int, status check.Status, last time.Time) {
	r.t.Helper()
	for i := n - 1; i >= 0; i-- {
		res := check.Result{CheckName: "api", At: last.Add(-time.Duration(i) * time.Second), Status: status}
		if err := r.s.Record(context.Background(), res); err != nil {
			r.t.Fatal(err)
		}
	}
	r.count += n
}

func TestMaintenanceRestartMidWindowAfterKnownDownSendsNoDuplicate(t *testing.T) {
	for _, n := range []int{50, 99, 100, 101} {
		t.Run(time.Duration(n).String(), func(t *testing.T) {
			r := newRig(t)
			r.addWindow(10, 2)
			r.start()
			r.feed(9, 30, down)
			r.want(down)
			r.stop()
			r.record(n, down, time.Date(2030, 1, 7, 10, 50, 0, 0, time.UTC))
			r.clock.set(11, 0)
			r.start()
			r.feed(11, 5, down)
			r.want(down)
			r.feed(12, 30, down)
			r.want(down)
		})
	}
}

func TestMaintenanceRestartAfterManyUpResultsStillOwesTheRecovery(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, down)
	r.want(down)
	r.stop()
	r.record(101, up, time.Date(2030, 1, 7, 10, 50, 0, 0, time.UTC))
	r.clock.set(11, 0)
	r.start()
	r.feed(11, 5, up)
	r.want(down)
	r.feed(12, 30, up)
	r.want(down, up)
}

func TestMaintenanceRestartAcrossWindowEndStillOwesTheDown(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, up)
	r.feed(10, 30, down)
	r.want()
	r.stop()
	r.clock.set(12, 30)
	r.start()
	r.feed(12, 35, down)
	r.want(down)
}

func TestMaintenanceRestartAcrossMuteEndStillOwesTheDown(t *testing.T) {
	r := newRig(t)
	if err := r.s.SetMute(context.Background(), "api", time.Date(2030, 1, 7, 11, 0, 0, 0, time.UTC), false); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.feed(9, 30, down)
	r.want()
	r.stop()
	r.clock.set(11, 30)
	r.start()
	r.feed(11, 35, down)
	r.want(down)
}

func TestMaintenanceDefinitionEditDuringWindowSendsNoDuplicate(t *testing.T) {
	r := newRig(t)
	r.addWindow(10, 2)
	r.start()
	r.feed(9, 30, down)
	r.want(down)
	r.record(150, down, time.Date(2030, 1, 7, 10, 50, 0, 0, time.UTC))
	r.clock.set(11, 0)
	before := r.runner.entered.Load()
	c, _ := r.s.Checks(context.Background())
	c[0].URL = "http://example.org"
	if err := r.s.UpdateCheck(context.Background(), c[0]); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the restarted loop", func() bool { return r.runner.entered.Load() > before })
	r.feed(11, 5, down)
	r.feed(12, 30, down)
	r.want(down)
}

func TestMaintenanceWindowAddedWhileRunningHoldsAlerts(t *testing.T) {
	r := newRig(t)
	r.start()
	r.feed(9, 30, up)
	r.addWindow(10, 2)
	time.Sleep(50 * time.Millisecond)
	r.feed(10, 30, down)
	r.want()
}

func TestMaintenanceWindowShortenedWhileRunningEndsSuppression(t *testing.T) {
	r := newRig(t)
	w := r.addWindow(10, 4)
	r.slowChecks()
	r.start()
	r.feed(10, 30, down)
	r.clock.set(12, 30)
	time.Sleep(100 * time.Millisecond)
	r.want()
	w.Duration = 2 * time.Hour
	if err := r.s.UpdateMaintenanceWindow(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the alert after shortening", func() bool { return len(r.alerts()) == 1 })
}

func TestMaintenanceLeavesNoGoroutinesBehind(t *testing.T) {
	r := newRig(t)
	w := r.addWindow(10, 2)
	r.slowChecks()
	// Warm the store so its own goroutines are part of the baseline.
	r.s.Checks(context.Background())
	baseline := runtime.NumGoroutine()
	r.start()
	r.feed(10, 30, down)
	running := runtime.NumGoroutine()
	if err := r.s.DeleteMaintenanceWindow(context.Background(), "api", w.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the alert after deleting the window", func() bool { return len(r.alerts()) == 1 })
	waitFor(t, "goroutines to settle after the delete", func() bool { return runtime.NumGoroutine() <= running })
	r.stop()
	waitFor(t, "goroutines to return to the baseline", func() bool { return runtime.NumGoroutine() <= baseline })
}

func TestMaintenanceSpringForwardKeepsElapsedDuration(t *testing.T) {
	r := newRig(t)
	// 2030-03-10: New York skips 02:00-03:00. 01:00 EST is 06:00Z; three hours
	// of elapsed time end at 09:00Z (05:00 EDT).
	if _, err := r.s.AddMaintenanceWindow(context.Background(), config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{time.Sunday}, StartMinute: 60, Duration: 3 * time.Hour, Zone: "America/New_York"}); err != nil {
		t.Fatal(err)
	}
	r.clock.setAt(time.Date(2030, 3, 10, 5, 0, 0, 0, time.UTC))
	r.start()
	r.feedAt(time.Date(2030, 3, 10, 5, 30, 0, 0, time.UTC), up)
	r.feedAt(time.Date(2030, 3, 10, 8, 30, 0, 0, time.UTC), down)
	r.want()
	r.feedAt(time.Date(2030, 3, 10, 9, 10, 0, 0, time.UTC), down)
	r.want(down)
}

func TestMaintenanceSkipsStartInTheSpringForwardGap(t *testing.T) {
	r := newRig(t)
	if _, err := r.s.AddMaintenanceWindow(context.Background(), config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{time.Sunday}, StartMinute: 150, Duration: 2 * time.Hour, Zone: "America/New_York"}); err != nil {
		t.Fatal(err)
	}
	r.clock.setAt(time.Date(2030, 3, 10, 5, 0, 0, 0, time.UTC))
	r.start()
	r.feedAt(time.Date(2030, 3, 10, 5, 30, 0, 0, time.UTC), up)
	// 03:45 EDT is 07:45Z; the 02:30 start does not exist that day.
	r.feedAt(time.Date(2030, 3, 10, 7, 45, 0, 0, time.UTC), down)
	r.want(down)
}

// PRESERVE: exact sequences of a manual mute without windows.
func TestMaintenanceManualMuteSequences(t *testing.T) {
	bg := context.Background()
	mute := func(r *maintenanceRig, until time.Time, was bool) {
		if err := r.s.SetMute(bg, "api", until, was); err != nil {
			t.Fatal(err)
		}
	}
	day := func(h, m int) time.Time { return time.Date(2030, 1, 7, h, m, 0, 0, time.UTC) }
	t.Run("mute down unmute", func(t *testing.T) {
		r := newRig(t)
		mute(r, day(11, 0), false)
		r.start()
		r.feed(9, 30, down)
		r.want()
		if err := r.s.Unmute(bg, "api", day(9, 31)); err != nil {
			t.Fatal(err)
		}
		r.feed(9, 40, down)
		r.want(down)
	})
	t.Run("down mute recover unmute", func(t *testing.T) {
		r := newRig(t)
		r.start()
		r.feed(9, 10, down)
		r.want(down)
		mute(r, day(11, 0), true)
		r.feed(9, 30, up)
		r.want(down)
		if err := r.s.Unmute(bg, "api", day(9, 31)); err != nil {
			t.Fatal(err)
		}
		r.feed(9, 40, up)
		r.want(down, up)
	})
	t.Run("mute expiring while down", func(t *testing.T) {
		r := newRig(t)
		mute(r, day(9, 45), false)
		r.start()
		r.feed(9, 30, down)
		r.want()
		r.feed(9, 50, down)
		r.want(down)
	})
}
