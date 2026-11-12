package tui_test

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/client"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/tui"
)

// TestRemotePingAndDNS adds a DNS monitor through the remote API, then shows
// the server's ping monitor pending while the daemon cannot send pings and
// the DNS answer from the server's history.
func TestRemotePingAndDNS(t *testing.T) {
	s, _, address := heartbeatServer(t)
	ctx := context.Background()
	clock := time.Date(2026, 11, 7, 3, 45, 0, 0, time.UTC)
	if err := s.SetPingPermission(ctx, config.PingDenied); err != nil {
		t.Fatal(err)
	}
	ping := config.Check{Name: "router", Kind: config.CheckPing, Host: "192.168.1.1", Interval: 30 * time.Second, Timeout: 2 * time.Second}
	if err := s.AddCheck(ctx, ping); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, check.Result{CheckName: "router", At: clock.Add(-time.Minute), Status: check.StatusUp, Latency: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	m := tui.New(client.New(address, remoteToken), func() time.Time { return clock }, tui.Options{Destination: address})
	m, _ = remoteUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = remoteUpdate(t, m, m.Init()())

	right, tab := tea.KeyPressMsg{Code: tea.KeyRight}, tea.KeyPressMsg{Code: tea.KeyTab}
	text := func(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Text: s} }
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"}, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, right, right, right)
	if !strings.Contains(remoteScreen(m), "! up serve cannot send pings. README › Ping says how to allow it.") {
		t.Fatalf("ping form:\n%s", remoteScreen(m))
	}
	m, _ = remotePress(t, m, right, tab, text("nas"), tab, text("nas.home.arpa"), tab, right, tab, text("[fd00::53]:53"), tab, text("fd00::10"))
	m, cmd := remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = settle(t, m, cmd)
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 2 {
		t.Fatalf("stored %+v %v\n%s", checks, err, remoteScreen(m))
	}
	for _, c := range checks {
		if c.Name == "nas" && (c.Kind != config.CheckDNS || c.Domain != "nas.home.arpa" || c.Record != "AAAA" || c.Resolver != "[fd00::53]:53" || c.Expected != "fd00::10") {
			t.Fatalf("stored %+v", c)
		}
	}
	if err := s.Record(ctx, check.Result{CheckName: "nas", At: clock.Add(-10 * time.Second), Status: check.StatusUp, Latency: 3 * time.Millisecond, Answer: "fd00::10", Answers: 3}); err != nil {
		t.Fatal(err)
	}
	m, _ = remoteUpdate(t, m, m.Init()())
	screen := remoteScreen(m)
	for _, want := range []string{"nas     dns   UP", "AAAA fd00::10 +2", "router  ping  PENDING", "cannot send pings"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("list lacks %q:\n%s", want, screen)
		}
	}
}
