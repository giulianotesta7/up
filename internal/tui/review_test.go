package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/giulianotesta7/up/internal/config"
)

func TestSMTPEditWarnsThatRemovingUsernameRemovesPassword(t *testing.T) {
	_, m := smtpSetup(t)
	m = smtpFill(t, m, "secret")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)

	m, _ = press(t, m, keyE, keyTab, keyTab, keyTab)
	m = replaceField(t, m, "user", "")
	requireText(t, m, "(the current password is removed because Username is empty)")
	if strings.Contains(view(m), "(leave empty to keep the current password)") {
		t.Fatalf("form still promises to keep the password:\n%s", view(m))
	}
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	servers, err := m.store.SMTPServers(context.Background())
	if err != nil || len(servers) != 1 || !servers[0].Password.Empty() {
		t.Fatalf("servers = %+v, %v, want the password removed as announced", servers, err)
	}
}

func TestEscFromAlertScreensReturnsToDetail(t *testing.T) {
	m := channelServers(t)
	c := config.Check{Name: "web", URL: "https://example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200}
	if err := m.store.AddCheck(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	for _, key := range []string{"s", "c"} {
		m, _ = press(t, m, keyEnter)
		requireText(t, m, "RECENT OUTAGES")
		m, _ = press(t, m, map[string]tea.KeyPressMsg{"s": keyS, "c": keyC}[key], keyEsc)
		requireText(t, m, "RECENT OUTAGES")
		m, _ = press(t, m, keyEsc)
	}
}

func TestChannelFormHintForMissingServerNamesBothEscapes(t *testing.T) {
	_, m := formStore(t)
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	m, _ = press(t, m, keyC, keyN)
	requireText(t, m, "(none — press esc twice, then s to add one)")
}
