package monitor

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/store"
)

type pruneStore struct {
	*store.Store
	mu      sync.Mutex
	cutoffs []time.Time
}

func (s *pruneStore) Prune(ctx context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cutoffs = append(s.cutoffs, before)
	return 0, nil
}

func (s *pruneStore) calls() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.cutoffs...)
}

func runFor(t *testing.T, s Storage, retention time.Duration, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	err := Run(ctx, Options{
		Store: s, Runner: &fakeRunner{calls: map[string]int{}},
		HeartbeatInterval: time.Hour, DiscoveryInterval: time.Hour,
		Retention: retention, PruneInterval: 5 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunPrunesOldResultsAtStartAndPeriodically(t *testing.T) {
	s := &pruneStore{Store: openStore(t)}
	before := time.Now()
	runFor(t, s, time.Hour, 200*time.Millisecond)
	cutoffs := s.calls()
	if len(cutoffs) < 2 {
		t.Fatalf("Prune called %d times, want at least 2 (start and periodic)", len(cutoffs))
	}
	if first := cutoffs[0]; first.Before(before.Add(-time.Hour)) || first.After(time.Now().Add(-time.Hour)) {
		t.Fatalf("first cutoff %v is not now minus the retention", first)
	}
}

func TestRunKeepsEverythingWithoutRetention(t *testing.T) {
	s := &pruneStore{Store: openStore(t)}
	runFor(t, s, 0, 100*time.Millisecond)
	if got := s.calls(); len(got) != 0 {
		t.Fatalf("Prune called %d times with retention 0, want none", len(got))
	}
}
