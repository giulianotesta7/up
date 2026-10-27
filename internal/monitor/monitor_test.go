package monitor

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// fakeRunner reports every check up, or canceled once ctx is done.
type fakeRunner struct {
	mu    sync.Mutex
	calls map[string]int
}

func (f *fakeRunner) Run(ctx context.Context, c config.Check) check.Result {
	f.mu.Lock()
	f.calls[c.Name]++
	f.mu.Unlock()
	if ctx.Err() != nil {
		return check.Result{CheckName: c.Name, At: time.Now(), Status: check.StatusCanceled}
	}
	return check.Result{CheckName: c.Name, At: time.Now(), Status: check.StatusUp, StatusCode: 200, Latency: time.Millisecond}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type sequenceRunner struct{ values chan check.Status }

func (r *sequenceRunner) Run(ctx context.Context, c config.Check) check.Result {
	select {
	case status := <-r.values:
		return check.Result{CheckName: c.Name, At: time.Now(), Status: status}
	case <-ctx.Done():
		return check.Result{Status: check.StatusCanceled}
	}
}

type recordingAlerter struct {
	mu       sync.Mutex
	statuses []check.Status
	names    []string
}

func (a *recordingAlerter) Notify(c config.Check, r check.Result) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.statuses = append(a.statuses, r.Status)
	a.names = append(a.names, c.Name)
}
func (a *recordingAlerter) snapshot() []check.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]check.Status(nil), a.statuses...)
}
func TestRunAlertTransitionsAndRestart(t *testing.T) {
	s := openStore(t)
	bg := context.Background()
	if err := s.AddSMTPServer(bg, config.SMTPServer{Name: "smtp", Host: "example.com", Port: 25, TLS: config.TLSNone, Sender: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(bg, config.Channel{Name: "mail", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
		t.Fatal(err)
	}
	c := config.Check{Name: "api", URL: "http://example.com", Interval: 10 * time.Millisecond, Timeout: time.Millisecond, ExpectedStatus: 200, Channel: "mail"}
	if err := s.AddCheck(bg, c); err != nil {
		t.Fatal(err)
	}
	runner := &sequenceRunner{values: make(chan check.Status, 10)}
	a := &recordingAlerter{}
	start := func() (context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(bg)
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Store: s, Runner: runner, Alerter: a, HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		}()
		return cancel, done
	}
	cancel, done := start()
	for i, status := range []check.Status{check.StatusDown, check.StatusUp, check.StatusUp, check.StatusDown} {
		runner.values <- status
		count := i + 1
		waitFor(t, "recorded result", func() bool { rows, _ := s.Recent(bg, c.Name, 10); return len(rows) >= count })
	}
	waitFor(t, "three alerts", func() bool { return len(a.snapshot()) == 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancel, done = start()
	runner.values <- check.StatusDown
	waitFor(t, "restart result", func() bool { rows, _ := s.Recent(bg, c.Name, 10); return len(rows) >= 5 })
	runner.values <- check.StatusUp
	waitFor(t, "recovery", func() bool { return len(a.snapshot()) == 4 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := a.snapshot(); !reflect.DeepEqual(got, []check.Status{check.StatusDown, check.StatusUp, check.StatusDown, check.StatusUp}) {
		t.Fatalf("alerts %v", got)
	}
}

func TestRunFirstUpAndNoChannelDoNotAlert(t *testing.T) {
	s := openStore(t)
	bg := context.Background()
	if err := s.AddSMTPServer(bg, config.SMTPServer{Name: "smtp", Host: "example.com", Port: 25, TLS: config.TLSNone, Sender: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(bg, config.Channel{Name: "mail", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []config.Check{{Name: "with-channel", URL: "http://example.com", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200, Channel: "mail"}, {Name: "without-channel", URL: "http://example.com", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200}} {
		if err := s.AddCheck(bg, c); err != nil {
			t.Fatal(err)
		}
	}
	a := &recordingAlerter{}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Store: s, Runner: &fakeRunner{calls: map[string]int{}}, Alerter: a, HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	for _, name := range []string{"with-channel", "without-channel"} {
		waitFor(t, name, func() bool { _, ok, _ := s.Latest(bg, name); return ok })
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := a.snapshot(); len(got) != 0 {
		t.Fatalf("alerts: %v", got)
	}
}

func TestRunSchedulesEveryCheckAndBeats(t *testing.T) {
	s := openStore(t)
	checks := []config.Check{
		{Name: "api", URL: "http://api.invalid", Interval: 20 * time.Millisecond, Timeout: 10 * time.Millisecond, ExpectedStatus: 200},
		{Name: "web", URL: "http://web.invalid", Interval: 30 * time.Millisecond, Timeout: 10 * time.Millisecond, ExpectedStatus: 200},
	}
	if err := s.SetChecks(context.Background(), checks); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{calls: map[string]int{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- Run(ctx, Options{
			Store:             s,
			Runner:            runner,
			HeartbeatInterval: 10 * time.Millisecond,
			Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()

	bg := context.Background()
	for _, name := range []string{"api", "web"} {
		waitFor(t, name+" to run three times", func() bool {
			r, _ := s.Recent(bg, name, 3)
			return len(r) == 3
		})
	}
	first, _, _ := s.LastBeat(bg)
	waitFor(t, "a later heartbeat", func() bool {
		at, ok, _ := s.LastBeat(bg)
		return ok && at.After(first)
	})

	stored, err := s.Checks(bg)
	if err != nil || !reflect.DeepEqual(stored, checks) {
		t.Fatalf("stored checks = %+v, %v, want %+v", stored, err, checks)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil after cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after cancel")
	}

	// Nothing is written after Run returns.
	before, _ := s.Recent(bg, "api", 1000)
	time.Sleep(50 * time.Millisecond)
	after, _ := s.Recent(bg, "api", 1000)
	if len(after) != len(before) {
		t.Fatalf("results grew from %d to %d after Run returned", len(before), len(after))
	}
	for _, r := range after {
		if r.Status != check.StatusUp {
			t.Fatalf("stored result %+v, want only up results (canceled ones are discarded)", r)
		}
	}
}

func TestRunRunsChecksImmediately(t *testing.T) {
	s := openStore(t)
	checks := []config.Check{{Name: "api", URL: "http://api.invalid", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200}}
	if err := s.SetChecks(context.Background(), checks); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Store: s, Runner: &fakeRunner{calls: map[string]int{}}, HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not stop")
		}
	})

	waitFor(t, "the first result without waiting an interval", func() bool {
		_, ok, _ := s.Latest(context.Background(), "api")
		return ok
	})
	waitFor(t, "the first heartbeat without waiting an interval", func() bool {
		_, ok, _ := s.LastBeat(context.Background())
		return ok
	})
}

type downRunner struct{}

func (downRunner) Run(ctx context.Context, c config.Check) check.Result {
	if ctx.Err() != nil {
		return check.Result{CheckName: c.Name, At: time.Now(), Status: check.StatusCanceled}
	}
	return check.Result{CheckName: c.Name, At: time.Now(), Status: check.StatusDown, Error: "connection refused"}
}

func TestRunDownWithoutChannelDoesNotAlert(t *testing.T) {
	s := openStore(t)
	bg := context.Background()
	if err := s.AddSMTPServer(bg, config.SMTPServer{Name: "smtp", Host: "example.com", Port: 25, TLS: config.TLSNone, Sender: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(bg, config.Channel{Name: "mail", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []config.Check{{Name: "with-channel", URL: "http://example.com", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200, Channel: "mail"}, {Name: "without-channel", URL: "http://example.com", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200}} {
		if err := s.AddCheck(bg, c); err != nil {
			t.Fatal(err)
		}
	}
	a := &recordingAlerter{}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Store: s, Runner: downRunner{}, Alerter: a, HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	for _, name := range []string{"with-channel", "without-channel"} {
		waitFor(t, name, func() bool { _, ok, _ := s.Latest(bg, name); return ok })
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !reflect.DeepEqual(a.names, []string{"with-channel"}) || !reflect.DeepEqual(a.statuses, []check.Status{check.StatusDown}) {
		t.Fatalf("alerts = %v %v, want one DOWN alert for with-channel", a.names, a.statuses)
	}
}

func alertingCheck(t *testing.T, s *store.Store, alertAfter, recoverAfter int) config.Check {
	t.Helper()
	bg := context.Background()
	if err := s.AddSMTPServer(bg, config.SMTPServer{Name: "smtp", Host: "example.com", Port: 25, TLS: config.TLSNone, Sender: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(bg, config.Channel{Name: "mail", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
		t.Fatal(err)
	}
	c := config.Check{Name: "api", URL: "http://example.com", Interval: 10 * time.Millisecond, Timeout: time.Millisecond, ExpectedStatus: 200, Channel: "mail", AlertAfter: alertAfter, RecoverAfter: recoverAfter}
	if err := s.AddCheck(bg, c); err != nil {
		t.Fatal(err)
	}
	return c
}

// feed runs the monitor until every status was recorded, then stops it.
func feed(t *testing.T, s *store.Store, a *recordingAlerter, already int, statuses ...check.Status) {
	t.Helper()
	bg := context.Background()
	runner := &sequenceRunner{values: make(chan check.Status, len(statuses))}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Store: s, Runner: runner, Alerter: a, HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	for i, status := range statuses {
		runner.values <- status
		want := already + i + 1
		waitFor(t, "recorded result", func() bool { rows, _ := s.Recent(bg, "api", 100); return len(rows) >= want })
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunAlertsAfterConsecutiveFailuresAndRecoveries(t *testing.T) {
	s := openStore(t)
	alertingCheck(t, s, 3, 2)
	a := &recordingAlerter{}
	D, U := check.StatusDown, check.StatusUp
	// Isolated failures and a failure streak of 2 never alert.
	feed(t, s, a, 0, D, U, D, D, U)
	if got := a.snapshot(); len(got) != 0 {
		t.Fatalf("alerts before threshold: %v", got)
	}
	// Three in a row alert once; one success is not yet a recovery.
	feed(t, s, a, 5, D, D, D, D, U, D)
	if got := a.snapshot(); !reflect.DeepEqual(got, []check.Status{D}) {
		t.Fatalf("alerts after outage: %v, want one down alert", got)
	}
	// Two successes in a row recover.
	feed(t, s, a, 11, U, U)
	if got := a.snapshot(); !reflect.DeepEqual(got, []check.Status{D, U}) {
		t.Fatalf("alerts after recovery: %v", got)
	}
}

func TestRunRestartKeepsFailureStreak(t *testing.T) {
	s := openStore(t)
	alertingCheck(t, s, 3, 1)
	a := &recordingAlerter{}
	D := check.StatusDown
	feed(t, s, a, 0, D, D) // daemon stops mid-streak
	feed(t, s, a, 2, D)    // the third failure after the restart alerts
	if got := a.snapshot(); !reflect.DeepEqual(got, []check.Status{D}) {
		t.Fatalf("alerts: %v, want one down alert", got)
	}
	feed(t, s, a, 3, D, D) // restart while alerting does not resend
	if got := a.snapshot(); len(got) != 1 {
		t.Fatalf("alert resent after restart: %v", got)
	}
}
