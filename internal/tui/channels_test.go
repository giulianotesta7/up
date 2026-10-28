package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/giulianotesta7/up/internal/config"
)

var keyC = tea.KeyPressMsg{Code: 'c', Text: "c"}

func channelServers(t *testing.T) Model {
	t.Helper()
	s, m := formStore(t)
	for _, name := range []string{"gmail", "relay"} {
		if err := s.AddSMTPServer(context.Background(), config.SMTPServer{Name: name, Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := m.Update(m.Init()())
	return next.(Model)
}
func TestChannelsCreateEdit(t *testing.T) {
	m := channelServers(t)
	s := m.store
	m, _ = press(t, m, keyC, keyN)
	requireText(t, m, "New channel", "Kind: ‹ email ›", "gmail · relay", "recipients separated by commas")
	m = typeText(t, m, "ops-team")
	m, _ = press(t, m, keyTab, keyRight, keyTab, keyRight, keyTab)
	next, _ := m.Update(tea.PasteMsg{Content: " ana@example.com, , oncall@example.com "})
	m = next.(Model)
	m, cmd := press(t, m, keyEnter)
	if cmd == nil {
		t.Fatal("no save command")
	}
	m = finishCommand(t, m, cmd)
	list, err := s.Channels(context.Background())
	if err != nil || len(list) != 1 || list[0].SMTPServer != "relay" || strings.Join(list[0].Recipients, ",") != "ana@example.com,oncall@example.com" {
		t.Fatalf("channel %v %v", list, err)
	}
	requireText(t, m, "Channels", "ops-team", "relay", "ana@example.com, oncall@example.com")
	m, _ = press(t, m, keyE)
	requireText(t, m, "Edit channel", "Name: ops-team")
	m, _ = press(t, m, keyShiftTab)
	m = typeText(t, m, "X")
	requireText(t, m, "Name: ops-team")
	m, _ = press(t, m, keyBack, keyTab, keyTab, keyRight, keyTab)
	m = replaceField(t, m, "ana@example.com, oncall@example.com", "new@example.com")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	list, err = s.Channels(context.Background())
	if err != nil || len(list) != 1 || list[0].SMTPServer != "gmail" || strings.Join(list[0].Recipients, ",") != "new@example.com" {
		t.Fatalf("edit %v %v", list, err)
	}
}
func TestChannelsNavigationAndMissingServer(t *testing.T) {
	s, m := formStore(t)
	if err := s.AddCheck(context.Background(), testChecks[0]); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	m, _ = press(t, m, keyEnter, keyC)
	requireText(t, m, "Channels", "No channels configured.", "↑/↓ select (k/j) · n new · e edit · d delete · esc back · q quit · ctrl+c quit")
	m, _ = press(t, m, keyN)
	requireText(t, m, "(none — press esc twice, then s to add one)")
	m = typeText(t, m, "ops")
	m, _ = press(t, m, keyTab, keyTab)
	next, _ = m.Update(tea.PasteMsg{Content: "gmail"})
	m = next.(Model)
	requireText(t, m, "(none — press esc twice, then s to add one)")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "ops@example.com")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "Cannot save channel: store: add channel: SMTP server is required")
	channels, err := s.Channels(context.Background())
	if err != nil || len(channels) != 0 {
		t.Fatalf("invalid save %v %v", channels, err)
	}
	m, _ = press(t, m, keyEsc, keyEsc)
	requireText(t, m, "RECENT OUTAGES")
	m, _ = press(t, m, keyEsc)
	requireText(t, m, "MONITOR")
}
func TestChannelsListAndDeletion(t *testing.T) {
	m := channelServers(t)
	s := m.store
	ctx := context.Background()
	for _, name := range []string{"ops-team", "unused"} {
		if err := s.AddChannel(ctx, config.Channel{Name: name, Kind: config.KindEmail, SMTPServer: "gmail", Recipients: []string{"ana@example.com", "web@example.com"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"api", "web"} {
		c := testChecks[0]
		c.Name = name
		c.Channel = "ops-team"
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	m, _ = press(t, m, keyC)
	requireText(t, m, "NAME", "KIND", "SMTP SERVER", "RECIPIENTS", "MONITORS", "ana@example.com, web@example.com")
	out := view(m)
	counts := map[string]string{"ops-team": "2", "unused": "0"}
	for _, line := range strings.Split(out, "\n") {
		for name, count := range counts {
			if strings.Contains(line, name) && strings.Contains(line, " "+count+" ") {
				delete(counts, name)
			}
		}
	}
	if len(counts) != 0 {
		t.Fatalf("counts: %s", out)
	}
	m, _ = press(t, m, keyD)
	requireText(t, m, `Delete channel "ops-team"? This cannot be undone.`)
	m, cmd := press(t, m, keyY)
	m = finishCommand(t, m, cmd)
	requireText(t, m, `Cannot delete channel "ops-team": used by monitors api, web`)
	channels, err := s.Channels(ctx)
	if err != nil || len(channels) != 2 {
		t.Fatalf("in use deleted %v %v", channels, err)
	}
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 2 || checks[0].Channel != "ops-team" || checks[1].Channel != "ops-team" {
		t.Fatalf("in use monitors changed %v %v", checks, err)
	}
	m, _ = press(t, m, keyEsc, keyDown, keyD)
	m, cmd = press(t, m, keyY)
	m = finishCommand(t, m, cmd)
	channels, err = s.Channels(ctx)
	if err != nil || len(channels) != 1 {
		t.Fatalf("unused not deleted %v %v", channels, err)
	}
}
func TestChannelsSaveGenerationGuard(t *testing.T) {
	m := channelServers(t)
	old := m.load()
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, "ops")
	m, _ = press(t, m, keyTab, keyTab, keyTab)
	m = typeText(t, m, "ops@example.com")
	m, cmd := press(t, m, keyEnter)
	result := cmd()
	next, refresh := m.Update(result)
	m = next.(Model)
	next, _ = m.Update(refresh())
	m = next.(Model)
	next, ignored := m.Update(old)
	m = next.(Model)
	if ignored != nil {
		t.Fatal("stale refresh restarted")
	}
	requireText(t, m, "ops")
	m, _ = press(t, m, keyE)
	m, cmd = press(t, m, keyEnter)
	before := view(m)
	next, ignored = m.Update(result)
	m = next.(Model)
	if ignored != nil || view(m) != before {
		t.Fatal("stale result changed draft")
	}
	_ = finishCommand(t, m, cmd)
}
