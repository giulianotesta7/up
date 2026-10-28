package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// quietSnapshot stores web (3 failures to alert, 2 successes to recover) with
// the given statuses, oldest first, and loads it back.
func quietSnapshot(t *testing.T, statuses ...check.Status) Snapshot {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := config.Check{Name: "web", URL: "https://example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200, AlertAfter: 3, RecoverAfter: 2}
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	for i, st := range statuses {
		r := check.Result{CheckName: "web", At: at("03:00:00").Add(time.Duration(i) * 30 * time.Second), Status: st, Latency: 80 * time.Millisecond}
		if st == check.StatusDown {
			r.Error = "timeout after 5s"
		}
		if err := s.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Beat(ctx, now); err != nil {
		t.Fatal(err)
	}
	snap, err := Load(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestStatusShowsFailureAndRecoveryProgress(t *testing.T) {
	D, U := check.StatusDown, check.StatusUp
	for _, tc := range []struct {
		name     string
		statuses []check.Status
		label    string
	}{
		{"isolated failure", []check.Status{U, D}, "FAILING 1/3"},
		{"two of three", []check.Status{U, D, D}, "FAILING 2/3"},
		{"alert fired", []check.Status{U, D, D, D}, "DOWN"},
		{"first success after alert", []check.Status{D, D, D, U}, "RECOVERING 1/2"},
		{"recovered", []check.Status{D, D, D, U, U}, "UP"},
		{"failure after recovered", []check.Status{D, D, D, U, U, D}, "FAILING 1/3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := quietSnapshot(t, tc.statuses...)
			if got := snap.Services[0].Label(); got != tc.label {
				t.Fatalf("Label() = %q, want %q", got, tc.label)
			}
			list := ansi.Strip(renderList(snap, 0, now, nil, 100))
			if !strings.Contains(list, tc.label) || !strings.Contains(list, "STATUS") {
				t.Fatalf("list missing %q:\n%s", tc.label, list)
			}
		})
	}
}

func TestDetailExplainsStreakAndRule(t *testing.T) {
	snap := quietSnapshot(t, check.StatusUp, check.StatusDown, check.StatusDown)
	detail := ansi.Strip(renderDetail(snap, snap.Services[0], now, nil, 100))
	for _, want := range []string{
		"Alerting: after 3 failures · recovers after 2 successes",
		"Status: FAILING 2/3",
		"Streak: 2 consecutive failures since Oct 25 03:00:30",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q:\n%s", want, detail)
		}
	}
	seeded := seededSnapshot(t, now)
	plain := ansi.Strip(renderDetail(seeded, seeded.Services[0], now, nil, 100))
	if strings.Contains(plain, "Alerting:") || strings.Contains(plain, "Streak:") {
		t.Fatalf("default thresholds changed the detail:\n%s", plain)
	}
}

func TestMonitorFormThresholds(t *testing.T) {
	s, m := formStore(t)
	m = fillForm(t, m, "web", "https://example.com", "30s", "5s", "200")
	m, _ = press(t, m, keyTab, keyTab) // channel, then Alert after
	requireText(t, m, "Alert after: ‹ 1 › failures in a row", "1 · 2 · 3 · 5 · 10", "Recover after: ‹ 1 › successes in a row")
	if strings.Contains(view(m), "Detection time") {
		t.Fatalf("hint shown for the default threshold:\n%s", view(m))
	}
	m, _ = press(t, m, keyRight, keyRight)
	requireText(t, m, "Alert after: ‹ 3 › failures in a row", "Detection time ≈ 3 × 30s = 1m30s before the first email")
	m, _ = press(t, m, keyTab, keyRight)
	requireText(t, m, "Recover after: ‹ 2 › successes in a row")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	checks, err := s.Checks(context.Background())
	if err != nil || len(checks) != 1 || checks[0].AlertAfter != 3 || checks[0].RecoverAfter != 2 {
		t.Fatalf("stored %+v, %v", checks, err)
	}
}

func TestEditFormLoadsThresholds(t *testing.T) {
	s, m := formStore(t)
	c := config.Check{Name: "web", URL: "https://example.com", Interval: time.Minute, Timeout: 5 * time.Second, ExpectedStatus: 200, AlertAfter: 5}
	if err := s.AddCheck(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	m, _ = press(t, m, keyE)
	requireText(t, m, "Alert after: ‹ 5 › failures in a row", "Recover after: ‹ 1 › successes in a row", "Detection time ≈ 5 × 1m0s = 5m0s")
}
