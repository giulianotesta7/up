package tui

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/monitor"
	"github.com/giulianotesta7/up/internal/store"
)

var keyE = tea.KeyPressMsg{Code: 'e', Text: "e"}
var keyD = tea.KeyPressMsg{Code: 'd', Text: "d"}
var keyY = tea.KeyPressMsg{Code: 'y', Text: "y"}

type manageStore struct {
	*store.Store
	updates, deletes int
	updated          config.Check
	failure          error
}

func (s *manageStore) UpdateCheck(ctx context.Context, c config.Check) error {
	s.updates++
	s.updated = c
	if s.failure != nil {
		return s.failure
	}
	return s.Store.UpdateCheck(ctx, c)
}
func (s *manageStore) DeleteCheck(ctx context.Context, name string) error {
	s.deletes++
	if s.failure != nil {
		return s.failure
	}
	return s.Store.DeleteCheck(ctx, name)
}
func managedModel(t *testing.T, count int) (*manageStore, Model) {
	t.Helper()
	s, _ := formStore(t)
	if err := s.SetChecks(context.Background(), testChecks[:count]); err != nil {
		t.Fatal(err)
	}
	f := &manageStore{Store: s}
	m := New(f, func() time.Time { return now }, Options{DatabasePath: "/shared/up.db"})
	next, _ := m.Update(m.Init()())
	return f, next.(Model)
}
func requireText(t *testing.T, m Model, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(view(m), want) {
			t.Fatalf("missing %q:\n%s", want, view(m))
		}
	}
}
func replaceEditURL(t *testing.T, m Model, url string) Model {
	t.Helper()
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	return replaceField(t, m, testChecks[0].URL, url)
}
func TestEditPrefillSaveOriginAndFixedName(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(map[bool]string{false: "list", true: "detail"}[detail], func(t *testing.T) {
			s, m := managedModel(t, 2)
			if detail {
				m, _ = press(t, m, keyEnter)
			}
			m, _ = press(t, m, keyE)
			requireText(t, m, "Edit monitor", "Name: api", "https://api.example.com/health", "30s", "5s", "200", "enter save", "esc cancel", "database: /shared/up.db")
			// Focus wraps among editable fields only, including backwards from URL.
			m, _ = press(t, m, keyShiftTab, keyShiftTab, keyShiftTab, keyShiftTab, keyShiftTab)
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyHome})
			m = typeText(t, m, "2")
			requireText(t, m, "Name: api", "2│200")
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDelete}, keyTab, keyTab, keyTab, keyTab, keyTab)
			m = replaceEditURL(t, m, "https://changed.example.com")
			m, _ = press(t, m, keyTab)
			m = replaceField(t, m, "30s", "1m")
			m, _ = press(t, m, keyTab)
			m = replaceField(t, m, "5s", "10s")
			m, _ = press(t, m, keyTab)
			m = replaceField(t, m, "200", "204")
			m, cmd := press(t, m, keyEnter)
			if s.updates != 0 {
				t.Fatal("synchronous write")
			}
			busy, again := press(t, m, keyEnter, keyEsc, keyN)
			if again != nil {
				t.Fatal("double submit or cancel during save")
			}
			m = finishCommand(t, busy, cmd)
			want := config.Check{Name: "api", URL: "https://changed.example.com", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204, AlertAfter: 1, RecoverAfter: 1}
			if s.updates != 1 || s.updated != want {
				t.Fatalf("updates=%d definition=%+v", s.updates, s.updated)
			}
			if strings.Contains(view(m), "Edit monitor") || strings.Contains(view(m), "Status:") != detail {
				t.Fatalf("wrong origin: %s", view(m))
			}
			if !detail {
				m, _ = press(t, m, keyEnter)
			}
			requireText(t, m, want.URL, "Interval: 1m0s", "Timeout: 10s", "Expected status: 204", "e edit", "d delete")
		})
	}
}
func TestEditRejectedAndCancelKeepDraftAndStorage(t *testing.T) {
	for _, kind := range []string{"invalid", "store error", "not found", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			s, m := managedModel(t, 1)
			before, err := Load(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			m, _ = press(t, m, keyEnter, keyE)
			url := "https://draft.example.com"
			want := ""
			switch kind {
			case "invalid":
				url = "ftp://draft.example.com"
				want = "must use http or https"
			case "store error":
				s.failure = errors.New("write unavailable")
				want = "write unavailable"
			case "not found":
				s.failure = errors.New("not found")
				want = "not found"
			}
			m = replaceEditURL(t, m, url)
			if kind == "cancel" {
				m, _ = press(t, m, keyEsc)
				requireText(t, m, "Status:")
			} else {
				var cmd tea.Cmd
				m, cmd = press(t, m, keyEnter)
				if cmd != nil {
					m = finishCommand(t, m, cmd)
				}
				requireText(t, m, "Edit monitor", url, want)
				next, poll := m.Update(snapshotMsg{generation: m.generation, err: errors.New("read unavailable")})
				m = next.(Model)
				if poll == nil {
					t.Fatal("polling stopped")
				}
				requireText(t, m, url, "read unavailable")
			}
			after, err := Load(context.Background(), s)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected edit changed storage: %v", err)
			}
			wantCalls := 0
			if kind == "store error" || kind == "not found" {
				wantCalls = 1
			}
			if s.updates != wantCalls {
				t.Fatalf("updates=%d", s.updates)
			}
		})
	}
}
func TestEditConcurrentlyDeletedMonitor(t *testing.T) {
	s, m := managedModel(t, 1)
	m, _ = press(t, m, keyE)
	m = replaceEditURL(t, m, "https://draft.example.com")
	if err := s.Store.DeleteCheck(context.Background(), "api"); err != nil {
		t.Fatal(err)
	}
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "Edit monitor", "https://draft.example.com", "not found")
	checks, err := s.Checks(context.Background())
	if err != nil || len(checks) != 0 || s.updates != 1 {
		t.Fatalf("edit recreated deleted monitor: %v %v", checks, err)
	}
}

func TestDeleteConfirmationCancelAndError(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{keyN, keyEsc, keyE, keyQ, keyEnter} {
		t.Run(key.String(), func(t *testing.T) {
			s, m := managedModel(t, 1)
			if err := s.Record(context.Background(), up("api", "03:44:30")); err != nil {
				t.Fatal(err)
			}
			storedBefore, err := Load(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			m, _ = press(t, m, keyEnter)
			before := view(m)
			m, _ = press(t, m, keyD)
			requireText(t, m, "api", "history", "cannot be undone", "y delete · n cancel", "database: /shared/up.db")
			m, cmd := press(t, m, key)
			if cmd != nil || s.deletes != 0 {
				t.Fatal("non-confirmation wrote or quit")
			}
			if key.String() == "n" || key.String() == "esc" {
				if view(m) != before {
					t.Fatal("cancel changed origin")
				}
			} else {
				requireText(t, m, "y delete · n cancel")
			}
			storedAfter, err := Load(context.Background(), s)
			if err != nil || !reflect.DeepEqual(storedBefore, storedAfter) {
				t.Fatal("cancel changed storage")
			}
		})
	}
	s, m := managedModel(t, 1)
	if err := s.Record(context.Background(), up("api", "03:44:30")); err != nil {
		t.Fatal(err)
	}
	storedBefore, err := Load(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	s.failure = errors.New("delete unavailable")
	m, _ = press(t, m, keyD)
	m, cmd := press(t, m, keyY)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "delete unavailable", "api", "y delete · n cancel")
	storedAfter, err := Load(context.Background(), s)
	if err != nil || !reflect.DeepEqual(storedBefore, storedAfter) || s.deletes != 1 {
		t.Fatal("failed deletion changed data")
	}
}
func TestDeleteSuccessNeighborAndEmpty(t *testing.T) {
	for _, count := range []int{1, 3} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			s, m := managedModel(t, count)
			if count == 3 {
				m, _ = press(t, m, keyJ)
			}
			m, _ = press(t, m, keyEnter, keyD)
			m, cmd := press(t, m, keyY)
			busy, again := press(t, m, keyY, keyEsc, keyN)
			if again != nil || s.deletes != 0 {
				t.Fatal("write was synchronous or double submit")
			}
			m = finishCommand(t, busy, cmd)
			if s.deletes != 1 || strings.Contains(view(m), "Status:") {
				t.Fatal("did not return to list")
			}
			if count == 1 {
				requireText(t, m, "No checks configured.")
			} else {
				m, _ = press(t, m, keyEnter)
				requireText(t, m, "https://new.example.com")
			}
		})
	}
}
func TestManageStaleResultsIgnored(t *testing.T) {
	for _, action := range []tea.KeyPressMsg{keyE, keyD} {
		t.Run(action.String(), func(t *testing.T) {
			_, m := managedModel(t, 2)
			oldRead := m.Init()()
			_, oldTick := m.Update(oldRead)
			m, _ = press(t, m, action)
			submit := keyEnter
			if action.String() == "d" {
				submit = keyY
			}
			m, cmd := press(t, m, submit)
			result := cmd()
			next, refresh := m.Update(result)
			m = next.(Model)
			next, _ = m.Update(refresh())
			m = next.(Model)
			for _, stale := range []tea.Msg{oldRead, oldTick()} {
				before := view(m)
				next, cmd = m.Update(stale)
				m = next.(Model)
				if cmd != nil || view(m) != before {
					t.Fatal("stale poll changed model")
				}
			}
			// A later operation is saving when the earlier result arrives.
			m, _ = press(t, m, keyE)
			m, cmd = press(t, m, keyEnter)
			before := view(m)
			next, ignored := m.Update(result)
			m = next.(Model)
			if ignored != nil || view(m) != before {
				t.Fatal("stale mutation dismissed newer draft")
			}
			_ = finishCommand(t, m, cmd)
		})
	}
}
func TestExistingKeysPreserved(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{keyUp, keyDown, keyJ, keyK, keyEnter, keyEsc, keyN, keyQ, keyCtrlC} {
		t.Run(key.String(), func(t *testing.T) {
			_, m := managedModel(t, 3)
			m, _ = press(t, m, keyDown)
			switch key.String() {
			case "esc":
				m, _ = press(t, m, keyEnter)
			}
			m, cmd := press(t, m, key)
			switch key.String() {
			case "up", "k":
				m, _ = press(t, m, keyEnter)
				requireText(t, m, "https://api.example.com/health")
			case "down", "j":
				m, _ = press(t, m, keyEnter)
				requireText(t, m, "https://new.example.com")
			case "enter":
				requireText(t, m, "Status:", "https://pay.example.com")
			case "esc":
				requireText(t, m, "MONITOR", "enter details")
			case "n":
				requireText(t, m, "New monitor", "30s", "5s", "200")
			case "q", "ctrl+c":
				if cmd == nil {
					t.Fatal("no quit")
				}
				if _, ok := cmd().(tea.QuitMsg); !ok {
					t.Fatal("not quit")
				}
			}
		})
	}
}
func TestEditDeleteRunningDaemon(t *testing.T) {
	var oldHits, newHits atomic.Int64
	oldServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { oldHits.Add(1); w.WriteHeader(200) }))
	defer oldServer.Close()
	newServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { newHits.Add(1); w.WriteHeader(204) }))
	defer newServer.Close()
	s, m := formStore(t)
	c := config.Check{Name: "live", URL: oldServer.URL, Interval: 30 * time.Millisecond, Timeout: 20 * time.Millisecond, ExpectedStatus: 200}
	if err := s.AddCheck(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- monitor.Run(ctx, monitor.Options{Store: s, Runner: check.New(), DiscoveryInterval: 5 * time.Millisecond, HeartbeatInterval: time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
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
	wait := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatal("daemon did not apply mutation")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait(func() bool { _, ok, _ := s.Latest(ctx, "live"); return ok })
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	m, _ = press(t, m, keyE)
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	m = replaceField(t, m, oldServer.URL, newServer.URL)
	m, _ = press(t, m, keyTab, keyTab, keyTab)
	m = replaceField(t, m, "200", "204")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	wait(func() bool {
		r, ok, _ := s.Latest(ctx, "live")
		return ok && r.StatusCode == 204 && r.Status == check.StatusUp
	})
	m, _ = press(t, m, keyD)
	m, cmd = press(t, m, keyY)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "No checks configured.")
	time.Sleep(100 * time.Millisecond)
	oldCount, newCount := oldHits.Load(), newHits.Load()
	time.Sleep(100 * time.Millisecond)
	if oldHits.Load() != oldCount || newHits.Load() != newCount {
		t.Fatal("deleted monitor still running")
	}
	history, err := s.Recent(ctx, "live", 100)
	if err != nil || len(history) != 0 {
		t.Fatalf("history survived deletion: %v %v", history, err)
	}
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 0 {
		t.Fatalf("definition survived: %v %v", checks, err)
	}
}
