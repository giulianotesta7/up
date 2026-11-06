package monitor

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

// lockedBuffer collects the daemon log.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// beatRig is a daemon with a heartbeat "backup" (period 1h, grace 10m,
// alerting through "mail") and an HTTP check "api" without a channel, on a
// fake clock that starts at 09:00.
type beatRig struct {
	t      *testing.T
	s      *store.Store
	clock  *fakeClock
	runner *fakeRunner
	alert  *recordingAlerter
	log    *lockedBuffer
	token  string
	cancel context.CancelFunc
	done   chan error
}

var beatStart = time.Date(2030, 1, 7, 9, 0, 0, 0, time.UTC)

const beatStep = 70 * time.Minute

func newBeatRig(t *testing.T, alertAfter, recoverAfter int) *beatRig {
	t.Helper()
	s := openStore(t)
	ctx := context.Background()
	key, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "up.db.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicKey(ctx, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	alertingCheck(t, s, 1, 1)
	api := config.Check{Name: "api", URL: "http://example.com", Interval: 10 * time.Millisecond, Timeout: time.Millisecond, ExpectedStatus: 200}
	if err := s.UpdateCheck(ctx, api); err != nil {
		t.Fatal(err)
	}
	c := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: time.Hour, Grace: 10 * time.Minute, Channel: "mail", AlertAfter: alertAfter, RecoverAfter: recoverAfter}
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	sealed, err := s.HeartbeatToken(ctx, "backup")
	if err != nil {
		t.Fatal(err)
	}
	token, err := key.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{}
	clock.setAt(beatStart)
	r := &beatRig{t: t, s: s, clock: clock, runner: &fakeRunner{calls: map[string]int{}}, alert: &recordingAlerter{}, log: &lockedBuffer{}, token: token}
	t.Cleanup(r.stop)
	return r
}

func (r *beatRig) start() {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan error, 1)
	go func() {
		r.done <- Run(ctx, Options{Store: r.s, Runner: r.runner, Alerter: r.alert, Now: r.clock.Now, HeartbeatInterval: time.Hour,
			DiscoveryInterval: 10 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(r.log, nil))})
	}()
	// The HTTP check's first run shows the loops started.
	waitFor(r.t, "the daemon to start", func() bool {
		r.runner.mu.Lock()
		defer r.runner.mu.Unlock()
		return r.runner.calls["api"] > 0
	})
}

func (r *beatRig) stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
		r.cancel = nil
	}
}

// ping records a ping (or a reported failure) at the fake time at, as the
// API does.
func (r *beatRig) ping(at time.Time, failed bool) {
	r.t.Helper()
	r.clock.setAt(at)
	found, err := r.s.PingHeartbeat(context.Background(), r.token, failed, at)
	if err != nil || !found {
		r.t.Fatalf("PingHeartbeat() = %v, %v", found, err)
	}
}

func (r *beatRig) results() []check.Result {
	r.t.Helper()
	got, err := r.s.Recent(context.Background(), "backup", 100)
	if err != nil {
		r.t.Fatal(err)
	}
	return got
}

func (r *beatRig) waitResults(n int) []check.Result {
	r.t.Helper()
	waitFor(r.t, "heartbeat results", func() bool { return len(r.results()) >= n })
	return r.results()
}

func (r *beatRig) waitAlerts(want ...check.Status) {
	r.t.Helper()
	waitFor(r.t, "alerts "+strings.Repeat("·", len(want)), func() bool { return len(r.alert.snapshot()) >= len(want) })
	// Absence of an extra alert: let a few polls pass.
	time.Sleep(50 * time.Millisecond)
	if got := r.alert.snapshot(); !reflect.DeepEqual(got, want) {
		r.t.Fatalf("alerts = %v, want %v", got, want)
	}
}

// beatAttempt is a delivery attempt without its time, newest first.
type beatAttempt struct {
	Monitor, Channel string
	Event            store.Event
	Outcome          store.Outcome
	Reason           string
}

func beatAttempts(t *testing.T, s *store.Store) []beatAttempt {
	t.Helper()
	var out []beatAttempt
	for _, a := range storedAttempts(t, s) {
		out = append(out, beatAttempt{a.Monitor, a.Channel, a.Event, a.Outcome, a.Reason})
	}
	return out
}

func missedAt(at time.Time, reason string) check.Result {
	return check.Result{CheckName: "backup", At: at, Status: check.StatusDown, Error: reason}
}

func TestHeartbeatStateMachine(t *testing.T) {
	r := newBeatRig(t, 1, 1)
	r.start()
	// WAITING: no ping yet, so no deadline however long it takes.
	r.clock.setAt(beatStart.Add(30 * 24 * time.Hour))
	time.Sleep(60 * time.Millisecond)
	if got := r.results(); len(got) != 0 {
		t.Fatalf("results while waiting = %+v", got)
	}
	r.waitAlerts()

	first := beatStart.Add(30 * 24 * time.Hour)
	r.ping(first, false)
	r.waitResults(1)
	r.waitAlerts()

	// At the deadline it is still up; just after it, a miss is recorded at
	// the deadline itself.
	r.clock.setAt(first.Add(beatStep))
	time.Sleep(60 * time.Millisecond)
	if got := r.results(); len(got) != 1 {
		t.Fatalf("results at the deadline = %+v", got)
	}
	r.clock.setAt(first.Add(beatStep + time.Second))
	got := r.waitResults(2)
	if want := missedAt(first.Add(beatStep), "no ping since 09:00 UTC"); got[0] != want {
		t.Fatalf("missed result = %+v, want %+v", got[0], want)
	}
	r.waitAlerts(down)

	// The next ping recovers it; /fail is down at once; a ping recovers again.
	recovered := first.Add(beatStep + 5*time.Minute)
	r.ping(recovered, false)
	r.waitResults(3)
	r.waitAlerts(down, up)
	failed := recovered.Add(20 * time.Minute)
	r.ping(failed, true)
	got = r.waitResults(4)
	if want := check.Fail("backup", failed); got[0] != want {
		t.Fatalf("failure result = %+v, want %+v", got[0], want)
	}
	r.waitAlerts(down, up, down)
	r.ping(failed.Add(time.Minute), false)
	r.waitResults(5)
	r.waitAlerts(down, up, down, up)

	r.runner.mu.Lock()
	calls := r.runner.calls["backup"]
	r.runner.mu.Unlock()
	if calls != 0 {
		t.Fatalf("the runner ran the heartbeat %d times; heartbeats make no outbound check", calls)
	}
	if strings.Contains(r.log.String(), r.token) {
		t.Fatal("the daemon log contains the ping token")
	}
}

func TestHeartbeatAlertAfterCountsMissedDeadlines(t *testing.T) {
	r := newBeatRig(t, 2, 2)
	r.start()
	r.ping(beatStart, false)
	r.waitResults(1)
	r.clock.setAt(beatStart.Add(beatStep + time.Second))
	r.waitResults(2)
	r.waitAlerts()
	// The second miss counts from the first.
	r.clock.setAt(beatStart.Add(2*beatStep + time.Second))
	got := r.waitResults(3)
	if want := missedAt(beatStart.Add(2*beatStep), "no ping since 09:00 UTC"); got[0] != want {
		t.Fatalf("second miss = %+v, want %+v", got[0], want)
	}
	r.waitAlerts(down)
	// Recover after 2: one ping is not enough.
	r.ping(beatStart.Add(2*beatStep+time.Minute), false)
	r.waitResults(4)
	r.waitAlerts(down)
	r.ping(beatStart.Add(2*beatStep+2*time.Minute), false)
	r.waitResults(5)
	r.waitAlerts(down, up)
}

func TestHeartbeatDeadlineSurvivesRestart(t *testing.T) {
	r := newBeatRig(t, 2, 1)
	r.start()
	r.ping(beatStart, false)
	r.waitResults(1)
	r.stop()
	// Five deadlines pass while the daemon is stopped: the first and the
	// latest are recorded when it starts again, which reaches alert after 2.
	r.clock.setAt(beatStart.Add(5*beatStep + time.Minute))
	r.start()
	got := r.waitResults(3)
	want := []check.Result{
		missedAt(beatStart.Add(5*beatStep), "no ping since 09:00 UTC"),
		missedAt(beatStart.Add(beatStep), "no ping since 09:00 UTC"),
		check.Ping("backup", beatStart),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results after restart = %+v, want %+v", got, want)
	}
	r.waitAlerts(down)
	// Another restart before the next deadline records nothing more.
	r.stop()
	r.start()
	time.Sleep(60 * time.Millisecond)
	if got := r.results(); len(got) != 3 {
		t.Fatalf("results after a second restart = %+v", got)
	}
	r.waitAlerts(down)
}

func TestHeartbeatEditAppliesTheNewPeriod(t *testing.T) {
	r := newBeatRig(t, 1, 1)
	r.start()
	r.ping(beatStart, false)
	r.waitResults(1)
	c := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: 3 * time.Hour, Grace: 10 * time.Minute, Channel: "mail"}
	if err := r.s.UpdateCheck(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	// Past the old deadline but not the new one.
	r.clock.setAt(beatStart.Add(2 * time.Hour))
	time.Sleep(80 * time.Millisecond)
	if got := r.results(); len(got) != 1 {
		t.Fatalf("results under the new period = %+v", got)
	}
	r.clock.setAt(beatStart.Add(3*time.Hour + 11*time.Minute))
	got := r.waitResults(2)
	if want := missedAt(beatStart.Add(3*time.Hour+10*time.Minute), "no ping since 09:00 UTC"); got[0] != want {
		t.Fatalf("missed = %+v, want %+v", got[0], want)
	}
	r.waitAlerts(down)
}

func TestHeartbeatAlertsAreHeldByMuteAndMaintenance(t *testing.T) {
	r := newBeatRig(t, 1, 1)
	ctx := context.Background()
	if err := r.s.SetMute(ctx, "backup", beatStart.Add(24*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	r.start()
	r.ping(beatStart, false)
	r.waitResults(1)
	r.clock.setAt(beatStart.Add(beatStep + time.Second))
	r.waitResults(2)
	waitFor(t, "the held down alert", func() bool { return len(storedAttempts(t, r.s)) == 1 })
	r.waitAlerts()
	if got := beatAttempts(t, r.s); !reflect.DeepEqual(got, []beatAttempt{{"backup", "mail", store.EventDown, store.OutcomeHeld, "muted"}}) {
		t.Fatalf("attempts = %+v", got)
	}
	if err := r.s.Unmute(ctx, "backup", r.clock.Now()); err != nil {
		t.Fatal(err)
	}
	r.waitAlerts(down)

	// A maintenance window holds the recovery and the next failure alike.
	if _, err := r.s.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "backup", Weekdays: []time.Weekday{0, 1, 2, 3, 4, 5, 6}, StartMinute: 10 * 60, Duration: 2 * time.Hour, Zone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	r.ping(beatStart.Add(beatStep+2*time.Minute), false)
	r.waitResults(3)
	waitFor(t, "the held recovery", func() bool { return len(storedAttempts(t, r.s)) == 2 })
	r.waitAlerts(down)
	if got := beatAttempts(t, r.s)[0]; got != (beatAttempt{"backup", "mail", store.EventRecovered, store.OutcomeHeld, "maintenance window"}) {
		t.Fatalf("newest attempt = %+v", got)
	}
	// After the window the owed recovery goes out; a ping late in the window
	// keeps the next deadline after it.
	r.ping(time.Date(2030, 1, 7, 11, 50, 0, 0, time.UTC), false)
	r.waitResults(4)
	r.waitAlerts(down)
	r.clock.setAt(time.Date(2030, 1, 7, 12, 0, 1, 0, time.UTC))
	r.waitAlerts(down, up)
}
