package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

func TestAlertStateLifecycle(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	for _, name := range []string{"a", "b"} {
		if err := s.AddCheck(ctx, config.Check{Name: name, URL: "http://example.com", Interval: time.Second, Timeout: time.Second, ExpectedStatus: 200}); err != nil {
			t.Fatal(err)
		}
	}
	if _, found, err := s.AlertState(ctx, "a"); err != nil || found {
		t.Fatalf("fresh state found=%v err=%v", found, err)
	}
	if err := s.SetAlertState(ctx, "missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown check: %v", err)
	}
	if states, err := s.AlertStates(ctx); err != nil || len(states) != 0 {
		t.Fatalf("states after rejection: %v %v", states, err)
	}
	for _, step := range []struct {
		name  string
		known bool
	}{{"a", true}, {"b", true}, {"a", false}} {
		if err := s.SetAlertState(ctx, step.name, step.known); err != nil {
			t.Fatal(err)
		}
	}
	if known, found, err := s.AlertState(ctx, "a"); err != nil || !found || known {
		t.Fatalf("a = %v %v %v, want false", known, found, err)
	}
	// The state survives reopening.
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	states, err := s2.AlertStates(ctx)
	if err != nil || len(states) != 2 || states["a"] || !states["b"] {
		t.Fatalf("reopened states = %v %v", states, err)
	}
	// Deleting a check forgets its state, so a new check of that name starts clean.
	if err := s2.DeleteCheck(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s2.AlertState(ctx, "b"); found {
		t.Fatal("state of a deleted check survived")
	}
}
