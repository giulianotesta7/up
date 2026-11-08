package tui_test

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/client"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/tui"
)

// TestRemoteCronHeartbeatFormAndDetail creates a cron heartbeat through the
// remote API at 80x24: next runs use the server's Defaults zone, the save
// stores the schedule and the detail names it.
func TestRemoteCronHeartbeatFormAndDetail(t *testing.T) {
	s, _, address := heartbeatServer(t)
	d := config.BuiltinDefaults()
	d.Zone = "America/Argentina/Buenos_Aires"
	if err := s.SetDefaults(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 25, 3, 45, 0, 0, time.UTC)
	m := tui.New(client.New(address, remoteToken), func() time.Time { return clock }, tui.Options{Destination: "https://up.example.com", Hostname: "up.example.com"})
	m, _ = remoteUpdate(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = remoteUpdate(t, m, m.Init()())

	right, tab := tea.KeyPressMsg{Code: tea.KeyRight}, tea.KeyPressMsg{Code: tea.KeyTab}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"}, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, right, right, tab,
		tea.KeyPressMsg{Text: "nightly"}, tab, right, tab, tea.KeyPressMsg{Text: "0 2 * * 1-5"})
	for _, want := range []string{"Schedule: ‹ Cron ›", "Zone       America/Argentina/Buenos_Aires (Defaults)",
		"Next runs  Mon 26 02:00 · Tue 27 02:00 · Wed 28 02:00"} {
		if !strings.Contains(remoteScreen(m), want) {
			t.Fatalf("form lacks %q:\n%s", want, remoteScreen(m))
		}
	}
	m, cmd := remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = settle(t, m, cmd)
	checks, err := s.Checks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var saved config.Check
	for _, c := range checks {
		if c.Name == "nightly" {
			saved = c
		}
	}
	if saved.Schedule != config.ScheduleCron || saved.Cron != "0 2 * * 1-5" || saved.Period != 0 {
		t.Fatalf("saved = %+v", saved)
	}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, tea.KeyPressMsg{Code: tea.KeyEnter})
	if out := remoteScreen(m); !strings.Contains(out, "nightly  heartbeat · cron 0 2 * * 1-5 · grace 1h") {
		t.Fatalf("detail:\n%s", out)
	}
	for _, line := range strings.Split(remoteScreen(m), "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Fatalf("line is %d columns: %q", w, line)
		}
	}
}
