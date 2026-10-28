package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/giulianotesta7/up/internal/config"
)

func TestMonitorChannelSelectorAndDetail(t *testing.T) {
	s, m := formStore(t)
	ctx := context.Background()
	server := config.SMTPServer{Name: "gmail", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}
	if err := s.AddSMTPServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	for _, c := range []config.Channel{{Name: "ops-team", Kind: config.KindEmail, SMTPServer: "gmail", Recipients: []string{"a@example.com", "b@example.com"}}, {Name: "infra", Kind: config.KindEmail, SMTPServer: "gmail", Recipients: []string{"a@example.com"}}} {
		if err := s.AddChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	m = fillForm(t, m, "api", "https://example.com", "30s", "5s", "200")
	m, _ = press(t, m, keyTab)
	requireText(t, m, "Channel: ‹ none ›", "none · infra · ops-team", "←/→ choose · tab/shift+tab focus · enter save · esc cancel · ctrl+c quit")
	next, _ = m.Update(tea.PasteMsg{Content: "ops-team"})
	m = next.(Model)
	requireText(t, m, "Channel: ‹ none ›")
	m, _ = press(t, m, keyRight, keyRight)
	requireText(t, m, "Channel: ‹ ops-team ›")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 1 || checks[0].Channel != "ops-team" {
		t.Fatalf("checks %v %v", checks, err)
	}
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "Channel: ops-team (email via gmail → 2 recipients)")
	if !strings.Contains(view(m), "Expected status: 200\nChannel: ops-team") {
		t.Fatal("channel line not after interval")
	}
	m, _ = press(t, m, keyE, keyShiftTab, keyShiftTab, keyShiftTab)
	requireText(t, m, "Channel: ‹ ops-team ›")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "Channel: ‹ none ›")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	checks, err = s.Checks(ctx)
	if err != nil || checks[0].Channel != "" {
		t.Fatalf("clear: %v %v", checks, err)
	}
	requireText(t, m, "Channel: none")
	m, _ = press(t, m, keyE, keyShiftTab, keyShiftTab, keyShiftTab, keyRight)
	requireText(t, m, "Channel: ‹ infra ›")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "Channel: infra (email via gmail → 1 recipient)")
}
func TestMonitorChannelHelpPreservesText(t *testing.T) {
	s, m := formStore(t)
	if err := s.AddCheck(context.Background(), testChecks[0]); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	requireText(t, m, "↑/↓ select (k/j) · enter details · n new monitor · e edit · d delete · m mute · u unmute · s SMTP servers · c channels · a animation · q quit · ctrl+c quit")
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "esc back · n new monitor · e edit · d delete · m mute · u unmute · s SMTP servers · c channels · a animation · q quit · ctrl+c quit")
}
