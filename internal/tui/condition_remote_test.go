package tui_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/heohlee/up/internal/client"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/tui"
)

// TestRemoteFormAddsConditions adds a monitor with a condition through the
// remote API, then checks the edit form and the detail read it back.
func TestRemoteFormAddsConditions(t *testing.T) {
	s, _, address := heartbeatServer(t)
	clock := time.Date(2026, 11, 7, 3, 45, 0, 0, time.UTC)
	m := tui.New(client.New(address, remoteToken), func() time.Time { return clock }, tui.Options{Destination: address})
	m, _ = remoteUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 50})
	m, _ = remoteUpdate(t, m, m.Init()())

	tab, space := tea.KeyPressMsg{Code: tea.KeyTab}, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	text := func(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Text: s} }
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"}, text("api"), tab, text("https://example.com"))
	for range 8 { // to the Advanced row
		m, _ = remotePress(t, m, tab)
	}
	m, _ = remotePress(t, m, space)
	for range 5 { // to + Condition
		m, _ = remotePress(t, m, tab)
	}
	m, _ = remotePress(t, m, space, tab, text("status"), tab, text("ok"))
	m, cmd := remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = settle(t, m, cmd)
	checks, err := s.Checks(context.Background())
	want := []config.Condition{{Kind: config.ConditionJSON, Path: "status", Value: "ok"}}
	if err != nil || len(checks) != 1 || !reflect.DeepEqual(checks[0].Conditions, want) {
		t.Fatalf("stored %+v %v\n%s", checks, err, remoteScreen(m))
	}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(remoteScreen(m), `Expect       JSON status = "ok"`) {
		t.Fatalf("detail:\n%s", remoteScreen(m))
	}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: 'e', Text: "e"})
	for _, want := range []string{"Condition 1: ‹ JSON value ›", "Path: status", "Equals: ok"} {
		if !strings.Contains(remoteScreen(m), want) {
			t.Fatalf("edit missing %q:\n%s", want, remoteScreen(m))
		}
	}
}
