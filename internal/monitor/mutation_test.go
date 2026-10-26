package monitor

import (
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
	"github.com/giulianotesta7/up/internal/store"
)

type loopCall struct {
	ctx context.Context
	def config.Check
}

// Context identity counts loop starts independently of check executions.
// A blocked call deliberately returns up even after cancellation, exercising
// the scheduler's late-result discard rather than relying on runner behavior.
type changingRunner struct {
	mu       sync.Mutex
	calls    []loopCall
	blockURL string
	release  chan struct{}
}

func (r *changingRunner) Run(ctx context.Context, c config.Check) check.Result {
	r.mu.Lock()
	r.calls = append(r.calls, loopCall{ctx, c})
	r.mu.Unlock()
	if c.URL == r.blockURL {
		<-r.release
	}
	return check.Result{CheckName: c.Name, At: time.Now(), Status: check.StatusUp, StatusCode: c.ExpectedStatus}
}

func (r *changingRunner) observed(name string) ([]loopCall, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var calls []loopCall
	contexts := map[context.Context]bool{}
	for _, call := range r.calls {
		if call.def.Name == name {
			calls = append(calls, call)
			contexts[call.ctx] = true
		}
	}
	return calls, len(contexts)
}

func runChanging(t *testing.T, s Storage, r Runner, logs *safeLog) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Store: s, Runner: r, HeartbeatInterval: time.Hour,
			DiscoveryInterval: 5 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Run failed to join every loop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func TestRunAppliesEveryDefinitionFieldWithoutRestartingSibling(t *testing.T) {
	for _, field := range []string{"url", "interval", "timeout", "status"} {
		t.Run(field, func(t *testing.T) {
			s := openStore(t)
			bg := context.Background()
			original := definition("api")
			original.Timeout = time.Millisecond
			if err := s.SetChecks(bg, []config.Check{original, definition("web")}); err != nil {
				t.Fatal(err)
			}
			r := &changingRunner{}
			var logs safeLog
			runChanging(t, s, r, &logs)
			waitFor(t, "initial results", func() bool {
				_, a, _ := s.Latest(bg, "api")
				_, b, _ := s.Latest(bg, "web")
				return a && b
			})
			changed := original
			switch field {
			case "url":
				changed.URL += "/changed"
			case "interval":
				changed.Interval = 20 * time.Millisecond
			case "timeout":
				changed.Timeout = 2 * time.Second
			case "status":
				changed.ExpectedStatus = 204
			}
			if err := s.UpdateCheck(bg, changed); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "edited definition to run immediately", func() bool {
				calls, starts := r.observed("api")
				return starts == 2 && calls[len(calls)-1].def == changed
			})
			waitFor(t, "edited result", func() bool { rs, _ := s.Recent(bg, "api", 100); return len(rs) >= 2 })
			time.Sleep(40 * time.Millisecond)
			calls, _ := r.observed("api")
			if calls[0].ctx.Err() == nil {
				t.Fatal("old loop not canceled")
			}
			if field == "interval" && len(calls) < 3 {
				t.Fatal("new interval not used")
			}
			for _, call := range calls[1:] {
				if call.def != changed {
					t.Fatalf("old definition still running: %+v", call.def)
				}
			}
			if sibling, starts := r.observed("web"); starts != 1 || len(sibling) != 1 || sibling[0].ctx.Err() != nil {
				t.Fatalf("unchanged sibling restarted/stopped: %v, starts %d", sibling, starts)
			}
			logs.mu.Lock()
			count := strings.Count(logs.text, "msg=\"check up\" check=api")
			logs.mu.Unlock()
			if count != 1 {
				t.Fatalf("got %d up transitions; edit must not reset status", count)
			}
		})
	}
}

func TestRunDeleteInFlightAndReaddFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	daemon, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { daemon.Close() })
	tui, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tui.Close() })
	bg := context.Background()
	c := definition("api")
	c.Interval, c.Timeout = 20*time.Millisecond, time.Millisecond
	if err := tui.AddCheck(bg, c); err != nil {
		t.Fatal(err)
	}
	if err := tui.Record(bg, check.Result{CheckName: "api", At: time.Now(), Status: check.StatusDown}); err != nil {
		t.Fatal(err)
	}
	r := &changingRunner{blockURL: c.URL, release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(r.release) }) }
	var logs safeLog
	stop := runChanging(t, daemon, r, &logs)
	// Release before the registered monitor cleanup even on test failure.
	t.Cleanup(unblock)
	waitFor(t, "blocked in-flight check", func() bool { calls, _ := r.observed("api"); return len(calls) == 1 })
	if err := tui.DeleteCheck(bg, "api"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "deleted loop cancellation", func() bool { calls, _ := r.observed("api"); return calls[0].ctx.Err() != nil })
	unblock()
	time.Sleep(50 * time.Millisecond)
	if calls, starts := r.observed("api"); len(calls) != 1 || starts != 1 {
		t.Fatal("deleted loop kept running")
	}
	if rs, err := tui.Recent(bg, "api", 100); err != nil || len(rs) != 0 {
		t.Fatalf("late orphan results: %v, %v", rs, err)
	}
	if os, err := tui.Outages(bg, "api", 100); err != nil || len(os) != 0 {
		t.Fatalf("deleted outages: %v, %v", os, err)
	}
	if cs, err := tui.Checks(bg); err != nil || len(cs) != 0 {
		t.Fatalf("deleted definition: %v, %v", cs, err)
	}
	if _, ok, err := tui.LastBeat(bg); err != nil || !ok {
		t.Fatalf("heartbeat lost: %v, %v", ok, err)
	}
	c.URL += "/fresh"
	c.Interval = time.Hour
	if err := tui.AddCheck(bg, c); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "fresh loop result", func() bool { _, ok, _ := tui.Latest(bg, "api"); return ok })
	if calls, starts := r.observed("api"); len(calls) != 2 || starts != 2 || calls[1].def != c {
		t.Fatalf("not a fresh loop: %v, %d", calls, starts)
	}
	stop()
	before, err := tui.Recent(bg, "api", 100)
	if err != nil || len(before) != 1 || before[0].Status != check.StatusUp {
		t.Fatalf("fresh history = %v, %v", before, err)
	}
	beat, _, _ := tui.LastBeat(bg)
	time.Sleep(30 * time.Millisecond)
	after, _ := tui.Recent(bg, "api", 100)
	afterBeat, _, _ := tui.LastBeat(bg)
	if !reflect.DeepEqual(before, after) || !beat.Equal(afterBeat) {
		t.Fatal("writes after Run returned")
	}
}

// Without a discovery poll, only RecordDefined can reject a deleted check's
// late result. This forces the separate-process delete/record race ordering.
func TestRunRecordDefinedBeforeDiscoveryNoticesDelete(t *testing.T) {
	s := openStore(t)
	bg := context.Background()
	c := definition("api")
	if err := s.AddCheck(bg, c); err != nil {
		t.Fatal(err)
	}
	r := &changingRunner{blockURL: c.URL, release: make(chan struct{})}
	var logs safeLog
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Store: s, Runner: r, HeartbeatInterval: time.Hour, DiscoveryInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	}()
	var release sync.Once
	unblock := func() { release.Do(func() { close(r.release) }) }
	t.Cleanup(func() {
		cancel()
		unblock()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not join")
		}
	})
	waitFor(t, "in-flight check", func() bool { calls, _ := r.observed("api"); return len(calls) == 1 })
	if err := s.DeleteCheck(bg, "api"); err != nil {
		t.Fatal(err)
	}
	unblock()
	time.Sleep(50 * time.Millisecond)
	if rs, err := s.Recent(bg, "api", 100); err != nil || len(rs) != 0 {
		t.Fatalf("orphan = %v, %v", rs, err)
	}
	if logs.contains("check up") {
		t.Fatal("logged transition for discarded result")
	}
}
