package tui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/monitor"
	"github.com/giulianotesta7/up/internal/store"
)

var (
	keyN        = tea.KeyPressMsg{Code: 'n', Text: "n"}
	keyTab      = tea.KeyPressMsg{Code: tea.KeyTab}
	keyShiftTab = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
)

func typeText(t *testing.T, m Model, text string) Model {
	t.Helper()
	m, _ = press(t, m, tea.KeyPressMsg{Text: text})
	return m
}

func replaceField(t *testing.T, m Model, old, value string) Model {
	t.Helper()
	for range []rune(old) {
		m, _ = press(t, m, keyBack)
	}
	return typeText(t, m, value)
}

func fillForm(t *testing.T, m Model, name, url, interval, timeout, status string) Model {
	t.Helper()
	m, _ = press(t, m, keyN)
	m = typeText(t, m, name)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, url)
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "30s", interval)
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "5s", timeout)
	m, _ = press(t, m, keyTab)
	return replaceField(t, m, "200", status)
}

func formStore(t *testing.T) (*store.Store, Model) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m := New(s, func() time.Time { return now })
	next, _ := m.Update(m.Init()())
	return s, next.(Model)
}

func finishCommand(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("save returned no command")
	}
	next, refresh := m.Update(cmd())
	m = next.(Model)
	// A save result starts a snapshot refresh; invalid input retains the form.
	if refresh != nil {
		msg := refresh()
		if _, ok := msg.(snapshotMsg); ok {
			next, _ = m.Update(msg)
			m = next.(Model)
		}
	}
	return m
}

func TestNewFormDefaultsAndInput(t *testing.T) {
	_, m := formStore(t)
	m, _ = press(t, m, keyN)
	for _, want := range []string{
		"New monitor", "Name", "HTTP/HTTPS URL", "Interval", "30s", "Timeout", "5s",
		"Expected status", "200", "tab/shift+tab", "enter save", "esc cancel", "ctrl+c quit",
	} {
		if !strings.Contains(view(m), want) {
			t.Fatalf("missing %q:\n%s", want, view(m))
		}
	}
	m, _ = press(t, m, keyQ, keyJ, keyK, keyN)
	m = typeText(t, m, "é界")
	m, _ = press(t, m, keyBack)
	if !strings.Contains(view(m), "qjkné") {
		t.Fatalf("literal input or Unicode backspace lost:\n%s", view(m))
	}
	m, _ = press(t, m, keyTab, keyShiftTab)
	m = typeText(t, m, "Z")
	if !strings.Contains(view(m), "qjknéZ") {
		t.Fatal("shift+tab did not restore name focus")
	}
	_, cmd := press(t, m, keyCtrlC)
	if cmd == nil {
		t.Fatal("ctrl+c did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("not quit")
	}
}

func TestFormCancelAndRejectedSavesPreserveStore(t *testing.T) {
	cases := []struct{ name, url, interval, timeout, status, want string }{
		{"", "https://example.com", "30s", "5s", "200", "name is required"},
		{"other", "", "30s", "5s", "200", "url is required"},
		{"other", "ftp://example.com", "30s", "5s", "200", "must use http or https"},
		{"other", "https://", "30s", "5s", "200", "must include a host"},
		{"other", "https://example.com", "oops", "5s", "200", "interval"},
		{"other", "https://example.com", "0s", "5s", "200", "greater than 0"},
		{"other", "https://example.com", "30s", "oops", "200", "timeout"},
		{"other", "https://example.com", "30s", "0s", "200", "greater than 0"},
		{"other", "https://example.com", "30s", "31s", "200", "must not exceed interval"},
		{"other", "https://example.com", "30s", "5s", "abc", "expected status"},
		{"other", "https://example.com", "30s", "5s", "99", "between 100 and 599"},
		{"other", "https://example.com", "30s", "5s", "600", "between 100 and 599"},
		{"api", "https://example.com", "30s", "5s", "200", "already exists"},
	}
	for _, tc := range cases {
		t.Run(tc.want+tc.name+tc.status, func(t *testing.T) {
			s, m := formStore(t)
			ctx := context.Background()
			if err := s.AddCheck(ctx, testChecks[0]); err != nil {
				t.Fatal(err)
			}
			if err := s.Record(ctx, down("api", "03:44:00")); err != nil {
				t.Fatal(err)
			}
			if err := s.Beat(ctx, now); err != nil {
				t.Fatal(err)
			}
			before, err := Load(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			m = fillForm(t, m, tc.name, tc.url, tc.interval, tc.timeout, tc.status)
			m, cmd := press(t, m, keyEnter)
			if cmd != nil {
				m = finishCommand(t, m, cmd)
			}
			if !strings.Contains(view(m), "New monitor") || !strings.Contains(view(m), tc.want) {
				t.Fatalf("missing retained form/error %q:\n%s", tc.want, view(m))
			}
			after, err := Load(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected save changed definitions/history/heartbeat: %+v", after)
			}
			m, _ = press(t, m, keyEsc)
			if strings.Contains(view(m), "New monitor") {
				t.Fatal("cancel retained form")
			}
			after, err = Load(ctx, s)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("cancel changed state: %v", err)
			}
		})
	}
}

func TestFormSavePersistsAndSelects(t *testing.T) {
	s, m := formStore(t)
	m = fillForm(t, m, "qjkné界", "https://example.com", "30s", "5s", "200")
	m, cmd := press(t, m, keyEnter)
	checks, err := s.Checks(context.Background())
	if err != nil || len(checks) != 0 {
		t.Fatal("Update wrote before its command ran")
	}
	// Repeated save and cancel while committing cannot start another write.
	saving, again := press(t, m, keyEnter, keyEsc, keyN)
	if again != nil {
		t.Fatal("saving accepted another action")
	}
	m = finishCommand(t, saving, cmd)
	if strings.Contains(view(m), "New monitor") || !strings.Contains(view(m), "qjkné界") {
		t.Fatalf("saved monitor not visible:\n%s", view(m))
	}
	checks, err = s.Checks(context.Background())
	if err != nil || len(checks) != 1 {
		t.Fatalf("checks %v, %v", checks, err)
	}
	c := checks[0]
	if c.Name != "qjkné界" || c.URL != "https://example.com" || c.Interval != 30*time.Second || c.Timeout != 5*time.Second || c.ExpectedStatus != 200 {
		t.Fatalf("persisted %+v", c)
	}
	m, _ = press(t, m, keyEnter)
	if !strings.Contains(view(m), "Status: PENDING") || !strings.Contains(view(m), "https://example.com") {
		t.Fatalf("selection/detail:\n%s", view(m))
	}
}

func TestSaveRejectsStaleRefreshAndResults(t *testing.T) {
	s, m := formStore(t)
	// Capture a read and its scheduled tick before saving.
	oldRead := m.load()
	_, oldTick := m.Update(oldRead)
	m = fillForm(t, m, "second", "https://example.com", "30s", "5s", "200")
	m, cmd := press(t, m, keyEnter)
	saved := cmd()
	next, refresh := m.Update(saved)
	m = next.(Model)
	next, _ = m.Update(refresh())
	m = next.(Model)
	next, staleCmd := m.Update(oldRead)
	m = next.(Model)
	if staleCmd != nil || !strings.Contains(view(m), "second") {
		t.Fatal("old snapshot overwrote saved entry or restarted polling")
	}
	next, staleCmd = m.Update(oldTick())
	m = next.(Model)
	if staleCmd != nil {
		t.Fatal("old tick restarted polling")
	}
	m, _ = press(t, m, keyN)
	m = typeText(t, m, "draft")
	next, staleCmd = m.Update(saved)
	m = next.(Model)
	if staleCmd != nil || !strings.Contains(view(m), "draft") {
		t.Fatal("old save result dismissed a new form")
	}
	checks, err := s.Checks(context.Background())
	if err != nil || len(checks) != 1 {
		t.Fatalf("checks %v %v", checks, err)
	}
}

func TestFormCancelWithoutSubmitAndReadError(t *testing.T) {
	s, m := formStore(t)
	m = fillForm(t, m, "draft", "http://example.com", "30s", "5s", "200")
	next, cmd := m.Update(snapshotMsg{err: errors.New("read unavailable")})
	m = next.(Model)
	if cmd == nil || !strings.Contains(view(m), "read unavailable") || !strings.Contains(view(m), "draft") {
		t.Fatal("poll read error lost draft or stopped polling")
	}
	m, _ = press(t, m, keyEsc)
	checks, err := s.Checks(context.Background())
	if err != nil || len(checks) != 0 {
		t.Fatalf("cancel wrote %v, %v", checks, err)
	}
}

func TestSaveFailureRetainsFormAndCanRetry(t *testing.T) {
	s, m := formStore(t)
	if err := s.AddCheck(context.Background(), testChecks[0]); err != nil {
		t.Fatal(err)
	}
	m = fillForm(t, m, "api", "http://example.com", "30s", "5s", "200")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	if !strings.Contains(view(m), "already exists") {
		t.Fatal("missing duplicate error")
	}
	// Wrap focus back to name and correct the duplicate in the retained form.
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "api", "unique")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	if strings.Contains(view(m), "New monitor") {
		t.Fatalf("retry failed: %s", view(m))
	}
	m, _ = press(t, m, keyEnter)
	if !strings.Contains(view(m), "http://example.com") || !strings.Contains(view(m), "unique") {
		t.Fatal("new entry was not selected")
	}
}

func TestFormCancelPreservesDetailAndPolling(t *testing.T) {
	s, m := formStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, testChecks[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, up("api", "03:44:30")); err != nil {
		t.Fatal(err)
	}
	// Exercise the existing poll command, not just a synthetic snapshot.
	next, load := m.Update(tickMsg{})
	m = next.(Model)
	next, tick := m.Update(load())
	m = next.(Model)
	if tick == nil || !strings.Contains(view(m), "UP") || !strings.Contains(view(m), "daemon not responding") {
		t.Fatalf("poll did not retain last status while stale: %s", view(m))
	}
	m, _ = press(t, m, keyEnter)
	before := view(m)
	m, _ = press(t, m, keyN)
	m = typeText(t, m, "discard")
	m, _ = press(t, m, keyEsc)
	if view(m) != before {
		t.Fatalf("cancel changed selected detail:\n%s", view(m))
	}
	m, _ = press(t, m, keyEsc, keyJ, keyK, keyEnter)
	if view(m) != before {
		t.Fatal("list/detail navigation changed")
	}
}

func TestSaveDatabaseErrorRetainsDraft(t *testing.T) {
	s, m := formStore(t)
	m = fillForm(t, m, "draft", "http://example.com", "30s", "5s", "200")
	s.Close()
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	for _, want := range []string{"New monitor", "draft", "http://example.com", "Cannot save monitor:", "database is closed", "error reading the database:"} {
		if !strings.Contains(view(m), want) {
			t.Fatalf("missing %q after save failure: %s", want, view(m))
		}
	}
	m, _ = press(t, m, keyEsc)
	if strings.Contains(view(m), "New monitor") {
		t.Fatal("error prevented cancel")
	}
}

func TestFormUnicodeCursorAndPaste(t *testing.T) {
	_, m := formStore(t)
	m, _ = press(t, m, keyN)
	m = typeText(t, m, "é界")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyLeft})
	m = typeText(t, m, "q")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDelete})
	next, _ := m.Update(tea.PasteMsg{Content: "日本\n語"})
	m = next.(Model)
	if !strings.Contains(view(m), "éq日本語") {
		t.Fatalf("Unicode editing: %s", view(m))
	}
}

func TestCreatedMonitorDiscoveredByDaemon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()
	s, m := formStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- monitor.Run(ctx, monitor.Options{
			Store: s, Runner: check.New(), HeartbeatInterval: time.Second,
			DiscoveryInterval: 10 * time.Millisecond,
			Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
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
			t.Error("daemon did not stop")
		}
	}()
	// Wait for a heartbeat so creation occurs after daemon startup.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, ok, err := s.LastBeat(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no heartbeat")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m = fillForm(t, m, "live", srv.URL, "30s", "5s", "204")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	for {
		r, ok, err := s.Latest(ctx, "live")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			if r.Status != check.StatusUp || r.StatusCode != 204 {
				t.Fatalf("result %+v", r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not discover monitor")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
