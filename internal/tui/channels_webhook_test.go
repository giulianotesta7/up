package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/giulianotesta7/up/internal/alert"
	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

var keyLeft = tea.KeyPressMsg{Code: tea.KeyLeft}

const (
	hookURL    = "https://Hooks.example.com/services/SECRETPATH?token=SECRETQUERY"
	hookOrigin = "https://hooks.example.com"
)

func hookChannelFromStore(t *testing.T, m Model) config.Channel {
	t.Helper()
	list, err := m.store.Channels(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("channels %v %v", list, err)
	}
	return list[0]
}

func requireNoSecrets(t *testing.T, m Model) {
	t.Helper()
	v := view(m)
	for _, leak := range []string{"SECRETPATH", "SECRETQUERY", "tok3n", "n3w-tok"} {
		if strings.Contains(v, leak) {
			t.Fatalf("view shows %q:\n%s", leak, v)
		}
	}
}

func TestWebhookChannelCreateSealsAndNeverDisplaysSecrets(t *testing.T) {
	_, m := smtpSetup(t)
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, "ops-hook")
	m, _ = press(t, m, keyTab, keyRight, keyTab)
	m = typeText(t, m, hookURL)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "Authorization")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "tok3n")
	if v := view(m); strings.Contains(v, "tok3n") || !strings.Contains(v, "•••••") {
		t.Fatalf("auth value not masked:\n%s", v)
	}
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c := hookChannelFromStore(t, m)
	if c.Kind != config.KindWebhook || c.WebhookOrigin != hookOrigin || c.WebhookAuthName != "Authorization" || c.WebhookURL.Empty() || c.WebhookAuth.Empty() {
		t.Fatalf("stored %+v", c)
	}
	if strings.Contains(string(c.WebhookURL.Data), "SECRETPATH") || strings.Contains(string(c.WebhookAuth.Data), "tok3n") {
		t.Fatal("stored values are not sealed")
	}
	requireText(t, m, "Channels", "ops-hook", "webhook", hookOrigin, "Authorization")
	requireNoSecrets(t, m)
}

func TestWebhookChannelSealedToDaemonKey(t *testing.T) {
	k, m := smtpSetup(t)
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, "ops-hook")
	m, _ = press(t, m, keyTab, keyRight, keyTab)
	m = typeText(t, m, hookURL)
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c := hookChannelFromStore(t, m)
	if got, err := k.Open(c.WebhookURL); err != nil || got != hookURL {
		t.Fatalf("open = %q %v", got, err)
	}
	if c.WebhookAuthName != "" || !c.WebhookAuth.Empty() {
		t.Fatalf("auth stored without being entered: %+v", c)
	}
}

func TestWebhookChannelEditKeepReplaceClear(t *testing.T) {
	k, m := smtpSetup(t)
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, "ops-hook")
	m, _ = press(t, m, keyTab, keyRight, keyTab)
	m = typeText(t, m, hookURL)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "Authorization")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "tok3n")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)

	// Open the edit form: only the origin and "set" show, with keep selected.
	m, _ = press(t, m, keyE)
	requireText(t, m, "Edit channel", hookOrigin, "set", "‹ keep ›", "Authorization")
	requireNoSecrets(t, m)
	// Keep everything: stored values stay.
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c := hookChannelFromStore(t, m)
	if u, _ := k.Open(c.WebhookURL); u != hookURL {
		t.Fatalf("keep changed the URL: %q", u)
	}
	if a, _ := k.Open(c.WebhookAuth); a != "tok3n" {
		t.Fatalf("keep changed the auth value: %q", a)
	}

	// The URL offers keep and replace only; it cannot be cleared.
	m, _ = press(t, m, keyE, keyTab)
	m, _ = press(t, m, keyRight)
	requireText(t, m, "‹ replace ›")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "‹ keep ›")

	// Replace the auth value by typing over the keep selector.
	m, _ = press(t, m, keyTab, keyTab)
	m = typeText(t, m, "n3w-tok")
	if v := view(m); strings.Contains(v, "n3w-tok") || !strings.Contains(v, "•••••••") {
		t.Fatalf("replacement not masked:\n%s", v)
	}
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c = hookChannelFromStore(t, m)
	if a, _ := k.Open(c.WebhookAuth); a != "n3w-tok" {
		t.Fatalf("replace stored %q", a)
	}
	if u, _ := k.Open(c.WebhookURL); u != hookURL {
		t.Fatalf("URL changed by an auth replace: %q", u)
	}

	// Clear the auth value; the header name stays.
	m, _ = press(t, m, keyE, keyTab, keyTab, keyTab, keyRight, keyRight)
	requireText(t, m, "‹ clear ›")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c = hookChannelFromStore(t, m)
	if !c.WebhookAuth.Empty() || c.WebhookAuthName != "Authorization" || c.WebhookURL.Empty() {
		t.Fatalf("clear stored %+v", c)
	}
}

func TestWebhookChannelRejectsMissingKeyAndBadURL(t *testing.T) {
	s, m := formStore(t) // no daemon key yet
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, "ops-hook")
	m, _ = press(t, m, keyTab, keyRight, keyTab)
	m = typeText(t, m, hookURL)
	m, cmd := press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("saved without an encryption key")
	}
	requireText(t, m, "Cannot save channel", "encryption key")
	if list, _ := s.Channels(context.Background()); len(list) != 0 {
		t.Fatalf("stored %v", list)
	}

	_, m = smtpSetup(t)
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, "ops-hook")
	m, _ = press(t, m, keyTab, keyRight, keyTab)
	m = typeText(t, m, "ftp://user:SECRETPATH@host/x")
	m, cmd = press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("saved an invalid URL")
	}
	requireText(t, m, "Cannot save channel", "http or https")
	if strings.Contains(m.channelForm.err.Error(), "SECRETPATH") {
		t.Fatalf("error echoes the URL: %v", m.channelForm.err)
	}
}

func TestWebhookKindSelectorIsOfferedAndEmailStillWorks(t *testing.T) {
	m := channelServers(t)
	m, _ = press(t, m, keyC, keyN)
	requireText(t, m, "Kind: ‹ email ›", "SMTP server", "Recipients")
	m, _ = press(t, m, keyTab, keyRight)
	requireText(t, m, "Kind: ‹ webhook ›")
	m, _ = press(t, m, keyLeft)
	requireText(t, m, "Kind: ‹ email ›", "Recipients")
}

func TestEditAddsAuthToHookWithoutAuth(t *testing.T) {
	k, m := smtpSetup(t)
	got := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
	}))
	defer srv.Close()
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, "ops-hook")
	m, _ = press(t, m, keyTab, keyRight, keyTab)
	m = typeText(t, m, srv.URL+"/hook")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	if c := hookChannelFromStore(t, m); !c.WebhookAuth.Empty() {
		t.Fatalf("auth stored on create: %+v", c)
	}

	m, _ = press(t, m, keyE, keyTab, keyTab)
	m = typeText(t, m, "Authorization")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "tok3n")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c := hookChannelFromStore(t, m)
	if c.WebhookAuthName != "Authorization" || c.WebhookAuth.Empty() {
		t.Fatalf("typed auth value was dropped: %+v", c)
	}
	if v, err := k.Open(c.WebhookAuth); err != nil || v != "tok3n" {
		t.Fatalf("auth value = %q %v", v, err)
	}
	n := alert.NewNotifier(alert.Options{Store: m.store.(*store.Store), Key: k, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ch := config.Check{Name: "api", Channel: "ops-hook"}
	if err := n.Deliver(context.Background(), ch, check.Result{Status: check.StatusDown, At: now}); err != nil {
		t.Fatal(err)
	}
	if h := <-got; h != "tok3n" {
		t.Fatalf("Authorization header = %q", h)
	}
}

func TestEditHookReplaceURL(t *testing.T) {
	k, m := smtpSetup(t)
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, "ops-hook")
	m, _ = press(t, m, keyTab, keyRight, keyTab)
	m = typeText(t, m, hookURL)
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)

	m, _ = press(t, m, keyE, keyTab)
	m = typeText(t, m, "http://other.example.org:8080/NEWPATH")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	c := hookChannelFromStore(t, m)
	if c.WebhookOrigin != "http://other.example.org:8080" {
		t.Fatalf("origin = %q", c.WebhookOrigin)
	}
	if v, err := k.Open(c.WebhookURL); err != nil || v != "http://other.example.org:8080/NEWPATH" {
		t.Fatalf("url = %q %v", v, err)
	}
	if strings.Contains(string(c.WebhookURL.Data), "NEWPATH") {
		t.Fatal("URL not sealed")
	}
}
