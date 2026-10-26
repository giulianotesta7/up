package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

func definition(name string) config.Check {
	return config.Check{Name: name, URL: "http://example.invalid", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200}
}

func startMonitor(t *testing.T, s *store.Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Store: s, Runner: &fakeRunner{calls: map[string]int{}},
			HeartbeatInterval: 5 * time.Millisecond, DiscoveryInterval: 5 * time.Millisecond,
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("monitor did not join its loops")
		}
	})
}

func TestRunDiscoversDatabaseAdditionsWithoutRestart(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "preexisting"}[preexisting], func(t *testing.T) {
			s := openStore(t)
			bg := context.Background()
			if preexisting {
				if err := s.AddCheck(bg, definition("first")); err != nil {
					t.Fatal(err)
				}
			}
			startMonitor(t, s)
			waitFor(t, "heartbeat on empty or populated DB", func() bool {
				_, ok, _ := s.LastBeat(bg)
				return ok
			})
			if !preexisting {
				if err := s.AddCheck(bg, definition("first")); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "first stored result", func() bool {
				_, ok, _ := s.Latest(bg, "first")
				return ok
			})
			first, _, _ := s.Latest(bg, "first")
			if err := s.AddCheck(bg, definition("second")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "appended check", func() bool {
				_, ok, _ := s.Latest(bg, "second")
				return ok
			})
			// Several discovery polls, but no check interval has elapsed.
			time.Sleep(40 * time.Millisecond)
			for _, name := range []string{"first", "second"} {
				results, err := s.Recent(bg, name, 100)
				if err != nil || len(results) != 1 {
					t.Fatalf("%s results = %v, %v; want exactly one", name, results, err)
				}
			}
			after, _, _ := s.Latest(bg, "first")
			if !reflect.DeepEqual(after, first) {
				t.Fatal("existing loop restarted")
			}
			checks, err := s.Checks(bg)
			if err != nil || !reflect.DeepEqual(checks, []config.Check{definition("first"), definition("second")}) {
				t.Fatalf("definitions = %v, %v", checks, err)
			}
		})
	}
}

func TestRunInitialReadError(t *testing.T) {
	s := openStore(t)
	s.Close()
	err := Run(context.Background(), Options{Store: s, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err == nil || !strings.Contains(err.Error(), "store: checks:") {
		t.Fatalf("error = %v, want initial checks read error", err)
	}
}

// An embedded public store lets discovery fail once without bypassing its API.
type flakyStore struct {
	*store.Store
	mu    sync.Mutex
	reads int
}

func (s *flakyStore) Checks(ctx context.Context) ([]config.Check, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.reads == 2 {
		return nil, errors.New("temporary discovery failure")
	}
	return s.Store.Checks(ctx)
}

type safeLog struct {
	mu   sync.Mutex
	text string
}

func (b *safeLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.text += string(p)
	return len(p), nil
}

func (b *safeLog) contains(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Contains(b.text, s)
}

func TestRunRetriesDiscoveryErrors(t *testing.T) {
	s := openStore(t)
	f := &flakyStore{Store: s}
	var logs safeLog
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Store: f, Runner: &fakeRunner{calls: map[string]int{}},
			HeartbeatInterval: time.Hour, DiscoveryInterval: 5 * time.Millisecond,
			Logger: slog.New(slog.NewTextHandler(&logs, nil)),
		})
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("shutdown timed out")
		}
	}()
	waitFor(t, "logged discovery error", func() bool {
		return logs.contains("temporary discovery failure")
	})
	if err := s.AddCheck(context.Background(), definition("later")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "retry result", func() bool {
		r, ok, _ := s.Latest(context.Background(), "later")
		return ok && r.Status == check.StatusUp
	})
}
