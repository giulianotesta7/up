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
