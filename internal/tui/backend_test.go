package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/config"
)

// aggregateStore panics on every per-resource read (nil embedded Store), so
// a refresh that does not use LoadSnapshot fails the test.
type aggregateStore struct {
	Store
	snap  Snapshot
	err   error
	calls int
	at    time.Time
}

func (a *aggregateStore) LoadSnapshot(_ context.Context, now time.Time) (Snapshot, error) {
	a.calls++
	a.at = now
	return a.snap, a.err
}

func TestRefreshUsesAggregateSnapshotLoader(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	want := Snapshot{Services: []Service{{Check: config.Check{Name: "api", URL: "https://api.example.com"}}}}
	s := &aggregateStore{snap: want}
	m := New(s, func() time.Time { return now })
	msg := m.Init()()
	if s.calls != 1 || !s.at.Equal(now) {
		t.Fatalf("calls = %d at = %v, want one call at the model clock", s.calls, s.at)
	}
	next, _ := m.Update(msg)
	if got := ansi.Strip(next.(Model).View().Content); !strings.Contains(got, "api") {
		t.Errorf("aggregate snapshot not shown:\n%s", got)
	}
}

func TestAggregateSnapshotErrorIsShownWithoutFallback(t *testing.T) {
	s := &aggregateStore{err: errors.New("server unreachable")}
	m := New(s, time.Now)
	next, _ := m.Update(m.Init()())
	got := ansi.Strip(next.(Model).View().Content)
	if !strings.Contains(got, "server unreachable") {
		t.Errorf("error not shown:\n%s", got)
	}
	if s.calls != 1 {
		t.Errorf("calls = %d, want 1 (no silent retry through other reads)", s.calls)
	}
}

func TestDestinationReplacesDatabaseFooter(t *testing.T) {
	s := &aggregateStore{}
	m := New(s, time.Now, Options{DatabasePath: "/shared/up.db", Destination: "https://up.acme.com"})
	next, _ := m.Update(m.Init()())
	got := ansi.Strip(next.(Model).View().Content)
	if !strings.HasSuffix(got, "remote · https://up.acme.com") || strings.Contains(got, "database:") {
		t.Errorf("remote footer wrong:\n%s", got)
	}
}

func TestDestinationDisplayIsSanitized(t *testing.T) {
	m := New(&aggregateStore{}, time.Now, Options{Destination: "https://up.acme.com\x1b[31m\nevil"})
	next, _ := m.Update(m.Init()())
	got := next.(Model).View().Content
	if strings.Contains(got, "\x1b[31m") || strings.Contains(ansi.Strip(got), "\nevil") {
		t.Errorf("destination leaked control sequences: %q", got)
	}
}

func TestLocalFooterUnchangedWithoutDestination(t *testing.T) {
	m := New(&aggregateStore{}, time.Now, Options{DatabasePath: "/shared/up.db"})
	next, _ := m.Update(m.Init()())
	if got := ansi.Strip(next.(Model).View().Content); !strings.HasSuffix(got, "database: /shared/up.db") {
		t.Errorf("local footer changed:\n%s", got)
	}
}

func TestRunStoreAcceptsAnyStore(t *testing.T) {
	var _ func(context.Context, Store, ...Options) error = RunStore
	var _ tea.Model = New(&aggregateStore{}, time.Now)
}
