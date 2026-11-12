package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

func TestCopyNameTakesTheNextFreeName(t *testing.T) {
	names := func(list ...string) Snapshot {
		var snap Snapshot
		for _, n := range list {
			snap.Services = append(snap.Services, Service{Check: config.Check{Name: n}})
		}
		return snap
	}
	for _, tc := range []struct {
		taken []string
		want  string
	}{
		{[]string{"api"}, "api copy"},
		{[]string{"api", "api copy"}, "api copy 2"},
		{[]string{"api", "api copy", "api copy 2"}, "api copy 3"},
		{[]string{"api", "api copy 2"}, "api copy"},
		{[]string{"api", "API copy"}, "api copy"},
	} {
		if got := copyName(names(tc.taken...), "api"); got != tc.want {
			t.Errorf("taken %v: %q, want %q", tc.taken, got, tc.want)
		}
	}
}

// duplicateStore is a real store with an HTTP and a TCP monitor.
func duplicateStore(t *testing.T) (*store.Store, Model) {
	t.Helper()
	s, m := formStore(t)
	ctx := context.Background()
	for _, c := range []config.Check{
		{Name: "api", URL: "https://example.com", Interval: 45 * time.Second, Timeout: 7 * time.Second, ExpectedStatus: 204, AlertAfter: 3, RecoverAfter: 2, TLSOff: true, Method: "PUT", ExpectText: "ok"},
		{Name: "db", Kind: config.CheckTCP, Host: "10.0.0.5", Port: 5432, Interval: time.Minute, Timeout: 2 * time.Second, AlertAfter: 2, RecoverAfter: 4},
	} {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := m.Update(m.Init()())
	return s, next.(Model)
}

func storedByName(t *testing.T, s *store.Store, name string) (config.Check, bool) {
	t.Helper()
	checks, err := s.Checks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		if c.Name == name {
			return c, true
		}
	}
	return config.Check{}, false
}

func TestDuplicateHTTPMonitorFromTheList(t *testing.T) {
	s, m := duplicateStore(t)
	m, _ = press(t, m, keyY)
	requireText(t, m, "New monitor · copy of api", "Name: api copy│", "https://example.com", "45s", "7s", "204", "‹ 3 › failures", "‹ 2 › successes", "‹ off ›", "Method: ‹ PUT ›", "Expected text: ok")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	got, ok := storedByName(t, s, "api copy")
	want, _ := storedByName(t, s, "api")
	want.Name = "api copy"
	if !ok || !got.SameDefinition(want) {
		t.Fatalf("copy = %+v, want %+v", got, want)
	}
}

func TestDuplicateTCPMonitor(t *testing.T) {
	s, m := duplicateStore(t)
	m, _ = press(t, m, keyDown, keyY)
	requireText(t, m, "New monitor · copy of db", "Name: db copy│", "Host: 10.0.0.5", "Port: 5432", "‹ TCP ›")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	got, ok := storedByName(t, s, "db copy")
	want, _ := storedByName(t, s, "db")
	want.Name = "db copy"
	if !ok || !got.SameDefinition(want) {
		t.Fatalf("copy = %+v, want %+v", got, want)
	}
}

func TestDuplicateCancelAndEditingTheCopy(t *testing.T) {
	s, m := duplicateStore(t)
	before, _ := s.Checks(context.Background())
	m, _ = press(t, m, keyY)
	m = typeText(t, m, "y") // a typed y is text, not the key
	requireText(t, m, "Name: api copyy│")
	m, _ = press(t, m, keyEsc)
	if out := view(m); strings.Contains(out, "copy of") || strings.Contains(out, "New monitor") {
		t.Fatalf("form still open:\n%s", out)
	}
	after, _ := s.Checks(context.Background())
	if len(after) != len(before) || len(after) != 2 {
		t.Fatalf("cancel changed the store: %d → %d", len(before), len(after))
	}
	// Editing the copy's fields saves what was typed, and the original stays.
	m, _ = press(t, m, keyY, keyTab)
	m = replaceField(t, m, "https://example.com", "https://example.net")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	got, _ := storedByName(t, s, "api copy")
	orig, _ := storedByName(t, s, "api")
	if got.URL != "https://example.net" || orig.URL != "https://example.com" {
		t.Fatalf("copy %q original %q", got.URL, orig.URL)
	}
}

func TestDuplicateNothingSelected(t *testing.T) {
	m := modelWith(Snapshot{})
	m, _ = press(t, m, keyY)
	if m.form != nil {
		t.Fatal("y opened a form on an empty list")
	}
}

func TestDuplicateWorksInSplitAndAt80x24(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}} {
		_, m := duplicateStore(t)
		m = sized(m, size[0], size[1])
		split := m.splitMode()
		m, _ = press(t, m, keyY)
		out := view(m)
		if !strings.Contains(out, "New monitor · copy of api") || !strings.Contains(out, "Name: api copy") {
			t.Errorf("%v split=%v:\n%s", size, split, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Errorf("%v: line too wide: %q", size, line)
			}
		}
		if n := strings.Count(out, "\n") + 1; n > size[1] {
			t.Errorf("%v: %d lines", size, n)
		}
	}
}

func TestHelpListsDuplicateButTheFooterDoesNot(t *testing.T) {
	for _, size := range [][2]int{{120, 35}, {80, 24}} {
		m := sized(modelWith(seededSnapshot(t, now)), size[0], size[1])
		if strings.Contains(view(m), "duplicate") {
			t.Errorf("%v: duplicate is in the list footer:\n%s", size, view(m))
		}
		m, _ = press(t, m, keyHelp)
		requireText(t, m, "y        duplicate", "n        new monitor")
		if size[0] == 80 && ansi.StringWidth(strings.Split(view(m), "\n")[0]) > 80 {
			t.Errorf("popup wider than the terminal")
		}
	}
}

func TestDuplicateOnAFilteredListCopiesTheSelectedVisibleMonitor(t *testing.T) {
	s, m := duplicateStore(t)
	m, _ = press(t, m, keySlash)
	m = typeKeys(t, m, "db")
	m, _ = press(t, m, keyEnter, keyY)
	requireText(t, m, "New monitor · copy of db", "Name: db copy│")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	got, ok := storedByName(t, s, "db copy")
	want, _ := storedByName(t, s, "db")
	want.Name = "db copy"
	if !ok || !got.SameDefinition(want) {
		t.Fatalf("copy = %+v, want %+v", got, want)
	}
	if _, ok := storedByName(t, s, "api copy"); ok {
		t.Fatal("copied the hidden monitor")
	}
}
