package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

var keyS = tea.KeyPressMsg{Code: 's', Text: "s"}
var keyRight = tea.KeyPressMsg{Code: tea.KeyRight}

func smtpSetup(t *testing.T) (*secret.Key, Model) {
	t.Helper()
	s, m := formStore(t)
	k, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetPublicKey(context.Background(), k.PublicKey()); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	return k, next.(Model)
}
func smtpFill(t *testing.T, m Model, password string) Model {
	t.Helper()
	m, _ = press(t, m, keyS, keyN)
	m = typeText(t, m, "gmail")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "smtp.example.com")
	m, _ = press(t, m, keyTab, keyTab, keyTab, keyTab)
	m = typeText(t, m, "user")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, password)
	m, _ = press(t, m, keyTab)
	return typeText(t, m, "up@example.com")
}
func TestSMTPCreateEditAndMask(t *testing.T) {
	k, m := smtpSetup(t)
	m = smtpFill(t, m, "sëcret")
	if strings.Contains(view(m), "sëcret") || !strings.Contains(view(m), "••••••") {
		t.Fatalf("password exposed or not masked: %s", view(m))
	}
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "SMTP servers", "gmail", "smtp.example.com", "set")
	s := m.store
	list, err := s.SMTPServers(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("servers %v %v", list, err)
	}
	got, err := k.Open(list[0].Password)
	if err != nil || got != "sëcret" {
		t.Fatalf("open %q %v", got, err)
	}
	m, _ = press(t, m, keyE)
	requireText(t, m, "Edit SMTP server", "(leave empty to keep the current password)")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	list, err = s.SMTPServers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err = k.Open(list[0].Password)
	if err != nil || got != "sëcret" {
		t.Fatalf("password lost %q %v", got, err)
	}
}
func TestSMTPNoKeyAndInvalidTLS(t *testing.T) {
	s, m := formStore(t)
	m, _ = press(t, m, keyS, keyN)
	requireText(t, m, "Passwords need the encryption key: start `up serve` once, then come back.", "(unavailable until `up serve` runs)")
	m = typeText(t, m, "gmail")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "smtp.example.com")
	m, _ = press(t, m, keyTab, keyTab, keyRight, keyRight, keyTab, keyTab)
	m = typeText(t, m, "user")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "up@example.com")
	requireText(t, m, "Sender: up@example.com")
	m, cmd := press(t, m, keyEnter)
	if cmd != nil {
		m = finishCommand(t, m, cmd)
	}
	requireText(t, m, `Cannot save SMTP server: store: add SMTP server: TLS "none" cannot be used with a username (the password would travel in plain text)`)
	servers, err := s.SMTPServers(context.Background())
	if err != nil || len(servers) != 0 {
		t.Fatalf("servers %v %v", servers, err)
	}
}
func TestSMTPNavigationStatesAndDelete(t *testing.T) {
	k, m := smtpSetup(t)
	s := m.store
	ctx := context.Background()
	sealed, err := secret.Seal(k.PublicKey(), "password")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := secret.Seal(other.PublicKey(), "password")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []config.SMTPServer{{Name: "gmail", Host: "smtp.example.com", Port: 587, TLS: config.TLSStartTLS, Username: "user", Password: sealed, Sender: "up@example.com"}, {Name: "old", Host: "old.example.com", Port: 465, TLS: config.TLSImplicit, Username: "user", Password: old, Sender: "up@example.com"}, {Name: "relay", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}} {
		if err := s.AddSMTPServer(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddChannel(ctx, config.Channel{Name: "ops-team", Kind: config.KindEmail, SMTPServer: "gmail", Recipients: []string{"ops@example.com"}}); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	m, _ = press(t, m, keyS)
	requireText(t, m, "NAME", "HOST", "PORT", "TLS", "USER", "PASSWORD", "set", "needs re-entry", "—")
	m, _ = press(t, m, keyD)
	requireText(t, m, `Delete SMTP server "gmail"? This cannot be undone.`)
	m, cmd := press(t, m, keyY)
	m = finishCommand(t, m, cmd)
	requireText(t, m, `Cannot delete SMTP server "gmail": used by channel ops-team`)
	list, err := s.SMTPServers(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("deleted in-use %v %v", list, err)
	}
	m, _ = press(t, m, keyEsc, keyDown, keyD)
	m, cmd = press(t, m, keyY)
	m = finishCommand(t, m, cmd)
	list, err = s.SMTPServers(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("delete unused %v %v", list, err)
	}
	m, _ = press(t, m, keyEsc)
	requireText(t, m, "No checks configured.")
	m, _ = press(t, m, keyN, keyEsc)
	requireText(t, m, "No checks configured.")
}
