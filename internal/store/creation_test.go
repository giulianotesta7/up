package store_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

var ctx = context.Background()
var at = time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)

func definition(name string) config.Check {
	return config.Check{Name: name, URL: "https://example.com/" + name, Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200}
}

type snapshot struct {
	Checks  []config.Check
	Recent  map[string][]check.Result
	Outages map[string][]store.Outage
	Beat    time.Time
	HasBeat bool
}

func observe(t *testing.T, s *store.Store) snapshot {
	t.Helper()
	checks, err := s.Checks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state := snapshot{Checks: checks, Recent: map[string][]check.Result{}, Outages: map[string][]store.Outage{}}
	// Include rejected names too: no history may appear for a rejected definition.
	for _, name := range []string{"api", "web", "new", "last", "bad"} {
		state.Recent[name], err = s.Recent(ctx, name, 100)
		if err != nil {
			t.Fatal(err)
		}
		state.Outages[name], err = s.Outages(ctx, name, 100)
		if err != nil {
			t.Fatal(err)
		}
		latest, ok, err := s.Latest(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if ok != (len(state.Recent[name]) > 0) || (ok && latest != state.Recent[name][0]) {
			t.Fatal("Latest disagrees with Recent")
		}
	}
	state.Beat, state.HasBeat, err = s.LastBeat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func seeded(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "up.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetChecks(ctx, []config.Check{definition("api"), definition("web")}); err != nil {
		t.Fatal(err)
	}
	for i, status := range []check.Status{check.StatusDown, check.StatusUp, check.StatusDown} {
		r := check.Result{CheckName: "api", At: at.Add(time.Duration(i) * time.Minute), Status: status, Latency: time.Millisecond, StatusCode: 503, Error: "sample failure"}
		if err := s.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Beat(ctx, at); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func assertUnchanged(t *testing.T, s *store.Store, before snapshot) {
	t.Helper()
	if after := observe(t, s); !reflect.DeepEqual(after, before) {
		t.Fatalf("state changed:\nbefore %+v\nafter %+v", before, after)
	}
}

func TestSetChecksStillReplacesAndKeepsHistory(t *testing.T) {
	s, _ := seeded(t)
	before := observe(t, s)
	// This replacement API intentionally remains distinct from AddCheck.
	if err := s.SetChecks(ctx, []config.Check{definition("web")}); err != nil {
		t.Fatal(err)
	}
	before.Checks = []config.Check{definition("web")}
	assertUnchanged(t, s, before)
	if err := s.SetChecks(ctx, nil); err != nil {
		t.Fatal(err)
	}
	before.Checks = nil
	assertUnchanged(t, s, before)
}

func TestAddCheckAppendsAndPersists(t *testing.T) {
	s, path := seeded(t)
	before := observe(t, s)
	if err := s.AddCheck(ctx, definition("new")); err != nil {
		t.Fatal(err)
	}
	before.Checks = append(before.Checks, definition("new"))
	assertUnchanged(t, s, before)
	reader, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	assertUnchanged(t, reader, before)
}

func TestAddCheckRejectsWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config.Check)
		want   string
	}{
		{"duplicate", func(c *config.Check) { *c = definition("api") }, "already exists"},
		{"name", func(c *config.Check) { c.Name = "" }, "name is required"},
		{"url", func(c *config.Check) { c.URL = "ftp://example.com" }, "must use http or https"},
		{"interval", func(c *config.Check) { c.Interval = 0 }, "interval must be greater than 0"},
		{"timeout", func(c *config.Check) { c.Timeout = -time.Second }, "timeout must be greater than 0"},
		{"timeout limit", func(c *config.Check) { c.Timeout = time.Minute }, "must not exceed interval"},
		{"status", func(c *config.Check) { c.ExpectedStatus = 600 }, "must be between 100 and 599"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := seeded(t)
			before := observe(t, s)
			c := definition("bad")
			tt.mutate(&c)
			if err := s.AddCheck(ctx, c); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			assertUnchanged(t, s, before)
		})
	}
}

func TestAddCheckRejectsEveryChangedExistingField(t *testing.T) {
	// A same-name check with identical or different fields is a duplicate.
	for _, field := range []string{"identical", "url", "interval", "timeout", "status"} {
		t.Run(field, func(t *testing.T) {
			s, _ := seeded(t)
			before := observe(t, s)
			c := definition("api")
			switch field {
			case "url":
				c.URL += "/other"
			case "interval":
				c.Interval = time.Minute
			case "timeout":
				c.Timeout = time.Second
			case "status":
				c.ExpectedStatus = 204
			}
			if err := s.AddCheck(ctx, c); err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("error = %v, want duplicate", err)
			}
			assertUnchanged(t, s, before)
		})
	}
}

func TestNewDatabaseAcceptsCreation(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AddCheck(ctx, definition("new")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Checks(ctx)
	if err != nil || !reflect.DeepEqual(got, []config.Check{definition("new")}) {
		t.Fatalf("Checks() = %+v, %v", got, err)
	}
}
