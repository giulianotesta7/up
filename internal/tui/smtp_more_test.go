package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/giulianotesta7/up/internal/config"
)

func TestSMTPDetailNavigationAndEmptyList(t *testing.T) {
	s, m := formStore(t)
	if err := s.AddCheck(context.Background(), testChecks[0]); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	m, _ = press(t, m, keyEnter, keyS)
	requireText(t, m, "SMTP servers", "No SMTP servers configured.", "↑/↓ select (k/j) · n new · e edit · d delete · esc back · q quit · ctrl+c quit")
	m, _ = press(t, m, keyEsc)
	requireText(t, m, "Status      PENDING", "RECENT OUTAGES")
}
func TestSMTPPasswordPasteAndSelectorPaste(t *testing.T) {
	_, m := smtpSetup(t)
	m, _ = press(t, m, keyS, keyN, keyTab, keyTab, keyTab)
	requireText(t, m, "TLS: ‹ starttls ›", "starttls · tls · none", "←/→ choose")
	next, _ := m.Update(tea.PasteMsg{Content: "none"})
	m = next.(Model)
	requireText(t, m, "TLS: ‹ starttls ›")
	m, _ = press(t, m, keyTab, keyTab)
	next, _ = m.Update(tea.PasteMsg{Content: "sécret\n"})
	m = next.(Model)
	if strings.Contains(view(m), "sécret") || !strings.Contains(view(m), "••••••") {
		t.Fatalf("paste not masked: %s", view(m))
	}
}
func TestSMTPPortErrorAndNoKeySave(t *testing.T) {
	s, m := formStore(t)
	m, _ = press(t, m, keyS, keyN)
	m = typeText(t, m, "relay")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "localhost")
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "587", "bad")
	m, cmd := press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("invalid port issued write")
	}
	requireText(t, m, "Cannot save SMTP server: port:")
	servers, err := s.SMTPServers(context.Background())
	if err != nil || len(servers) != 0 {
		t.Fatalf("invalid port wrote %v %v", servers, err)
	}
	m = replaceField(t, m, "bad", "25")
	m, _ = press(t, m, keyTab, keyRight, keyRight, keyTab, keyTab)
	m = typeText(t, m, "up@example.com")
	m, cmd = press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	servers, err = s.SMTPServers(context.Background())
	if err != nil || len(servers) != 1 || !servers[0].Password.Empty() || servers[0].TLS != config.TLSNone {
		t.Fatalf("no-key save %v %v", servers, err)
	}
}
func TestSMTPStaleSaveAndDeleteResults(t *testing.T) {
	_, m := smtpSetup(t)
	oldRead := m.load()
	m = smtpFill(t, m, "secret")
	m, cmd := press(t, m, keyEnter)
	result := cmd()
	next, refresh := m.Update(result)
	m = next.(Model)
	next, _ = m.Update(refresh())
	m = next.(Model)
	next, cmd = m.Update(oldRead)
	m = next.(Model)
	if cmd != nil {
		t.Fatal("stale read restarted poll")
	}
	requireText(t, m, "gmail")
	m, _ = press(t, m, keyE)
	m, cmd = press(t, m, keyEnter)
	before := view(m)
	next, ignored := m.Update(result)
	m = next.(Model)
	if ignored != nil || view(m) != before {
		t.Fatal("stale save dismissed draft")
	}
	m = finishCommand(t, m, cmd)
	m, _ = press(t, m, keyD)
	m, cmd = press(t, m, keyY)
	result = cmd()
	next, refresh = m.Update(result)
	m = next.(Model)
	next, _ = m.Update(refresh())
	m = next.(Model)
	requireText(t, m, "No SMTP servers configured.")
	next, ignored = m.Update(result)
	if ignored != nil || view(next.(Model)) != view(m) {
		t.Fatal("stale deletion changed view")
	}
}
