package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSMTPNoLoginCreateWithoutKey(t *testing.T) {
	s, m := formStore(t)
	m, _ = press(t, m, keyS, keyN)
	m = typeText(t, m, "relay")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "localhost")
	m, _ = press(t, m, keyTab, keyTab, keyRight, keyRight, keyTab)
	requireText(t, m, "No login required: ‹ No ›", "←/→ choose")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "No login required: ‹ Yes ›", "Username: (not required)", "Password: (not required)")
	if strings.Contains(view(m), "Passwords need the encryption key") {
		t.Fatalf("no-login form asks for a key: %s", view(m))
	}
	next, _ := m.Update(tea.PasteMsg{Content: "No"})
	m = next.(Model)
	requireText(t, m, "No login required: ‹ Yes ›")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "up@example.com")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "SMTP servers", "relay")
	servers, err := s.SMTPServers(context.Background())
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers = %+v, %v", servers, err)
	}
	server := servers[0]
	if server.Username != "" || !server.Password.Empty() || server.Host != "localhost" || server.Port != 587 || string(server.TLS) != "none" || server.Sender != "up@example.com" {
		t.Fatalf("unexpected no-login server: %+v", server)
	}
	m, _ = press(t, m, keyE)
	requireText(t, m, "No login required: ‹ Yes ›", "Username: (not required)")
	// Editing starts at Host: backward traversal also skips both credentials.
	m, _ = press(t, m, keyTab, keyTab, keyTab, keyTab)
	if m.smtpForm.focus != 6 {
		t.Fatalf("no-login navigation did not skip credentials: %d", m.smtpForm.focus)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if m.smtpForm.focus != 7 {
		t.Fatalf("backward navigation did not skip credentials: %d", m.smtpForm.focus)
	}
}

func TestSMTPNoLoginDiscardsCredentialsOnSave(t *testing.T) {
	for _, editing := range []bool{false, true} {
		name := "create"
		if editing {
			name = "edit"
		}
		t.Run(name, func(t *testing.T) {
			_, m := smtpSetup(t)
			m = smtpFill(t, m, "secret")
			if editing {
				var cmd tea.Cmd
				m, cmd = press(t, m, keyEnter)
				m = finishCommand(t, m, cmd)
				m, _ = press(t, m, keyE, keyTab, keyTab, keyTab, keyTab, keyTab)
				m = typeText(t, m, "replacement")
				m, _ = press(t, m, keyTab)
			}
			m, _ = press(t, m, keyShiftTab, keyShiftTab, keyShiftTab, keyRight)
			requireText(t, m, "No login required: ‹ Yes ›", "Username: (not required)", "Password: (not required)")
			if strings.Contains(view(m), "••") || strings.Contains(view(m), "keep the current password") {
				t.Fatalf("disabled credentials still shown: %s", view(m))
			}
			if editing {
				requireText(t, m, "Saving without login removes the stored username and password.")
			}
			// Switching back must restore authenticated drafts without changing TLS.
			m, _ = press(t, m, keyRight)
			requireText(t, m, "No login required: ‹ No ›", "Username: user", "TLS: ‹ starttls ›")
			m, _ = press(t, m, keyRight)
			m, cmd := press(t, m, keyEnter)
			m = finishCommand(t, m, cmd)
			requireText(t, m, "SMTP servers", "gmail")
			servers, err := m.store.SMTPServers(context.Background())
			if err != nil || len(servers) != 1 {
				t.Fatalf("servers = %+v, %v", servers, err)
			}
			if servers[0].Username != "" || !servers[0].Password.Empty() {
				t.Fatalf("no-login save retained credentials: %+v", servers[0])
			}
			m, _ = press(t, m, keyE)
			requireText(t, m, "No login required: ‹ Yes ›")
		})
	}
}
