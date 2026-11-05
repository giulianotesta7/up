package client

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

// formDriver presses keys on a real tui.Model.
type formDriver struct {
	t *testing.T
	m tui.Model
}

var (
	kEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	kEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	kTab   = tea.KeyPressMsg{Code: tea.KeyTab}
	kRight = tea.KeyPressMsg{Code: tea.KeyRight}
	kBack  = tea.KeyPressMsg{Code: tea.KeyBackspace}
	kC     = tea.KeyPressMsg{Code: 'c', Text: "c"}
	kN     = tea.KeyPressMsg{Code: 'n', Text: "n"}
	kE     = tea.KeyPressMsg{Code: 'e', Text: "e"}
)

func (d *formDriver) press(keys ...tea.KeyPressMsg) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		next, c := d.m.Update(k)
		d.m, cmd = next.(tui.Model), c
	}
	return cmd
}

func (d *formDriver) typ(s string) { d.press(tea.KeyPressMsg{Text: s}) }

func (d *formDriver) back(n int) {
	for range n {
		d.press(kBack)
	}
}

func (d *formDriver) screen() string { return ansi.Strip(d.m.View().Content) }

// save presses Enter, runs the save and the snapshot refresh it starts, and
// returns the screen afterwards (the form stays open on a rejected save).
func (d *formDriver) save() string {
	d.t.Helper()
	cmd := d.press(kEnter)
	if cmd == nil {
		d.t.Fatalf("Enter returned no command:\n%s", d.screen())
	}
	next, refresh := d.m.Update(cmd())
	d.m = next.(tui.Model)
	if refresh != nil {
		if msg := refresh(); msg != nil {
			next, _ = d.m.Update(msg)
			d.m = next.(tui.Model)
		}
	}
	return d.screen()
}

// rejects presses Enter and expects the form to refuse without saving.
func (d *formDriver) rejects() string {
	d.t.Helper()
	if cmd := d.press(kEnter); cmd != nil {
		d.t.Fatalf("a save was attempted:\n%s", d.screen())
	}
	return d.screen()
}

// webhookBackend is a model plus a way to read what was stored.
type webhookBackend struct {
	m   tui.Model
	s   *store.Store
	key *secret.Key
}

func localBackend(t *testing.T) webhookBackend {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	key, _, err := secret.LoadOrCreate(filepath.Join(dir, "up.db.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicKey(context.Background(), key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	m := tui.New(s, time.Now)
	next, _ := m.Update(m.Init()())
	return webhookBackend{m: next.(tui.Model), s: s, key: key}
}

func remoteBackend(t *testing.T) webhookBackend {
	t.Helper()
	c, s, key := realServer(t)
	m := tui.New(c, time.Now)
	next, _ := m.Update(m.Init()())
	return webhookBackend{m: next.(tui.Model), s: s, key: key}
}

func (b webhookBackend) channel(t *testing.T, name string) config.Channel {
	t.Helper()
	list, err := b.s.Channels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("channel %q not stored: %+v", name, list)
	return config.Channel{}
}

func (b webhookBackend) open(t *testing.T, s secret.Sealed) string {
	t.Helper()
	v, err := b.key.Open(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestWebhookChannelFormLocalAndRemote drives the channel form through the
// creation and edit rules of a webhook (auth optional and addable later; keep,
// replace and clear for the auth value; a URL replace re-derives the origin;
// the URL cannot be cleared; typing then erasing stores nothing) against the
// local store and against the client talking to the real API handlers.
func TestWebhookChannelFormLocalAndRemote(t *testing.T) {
	for name, backend := range map[string]func(*testing.T) webhookBackend{"local": localBackend, "remote": remoteBackend} {
		t.Run(name, func(t *testing.T) {
			b := backend(t)
			d := &formDriver{t: t, m: b.m}
			const secretURL = "https://hooks.example.com/a/SECRET"

			// Create without auth.
			d.press(kC, kN)
			d.typ("h1")
			d.press(kTab, kRight, kTab)
			d.typ(secretURL)
			if out := d.save(); strings.Contains(out, "required") || strings.Contains(out, "SECRET") {
				t.Fatalf("create:\n%s", out)
			}
			c := b.channel(t, "h1")
			if c.Kind != config.KindWebhook || c.WebhookOrigin != "https://hooks.example.com" || !c.WebhookAuth.Empty() || c.WebhookAuthName != "" {
				t.Fatalf("create stored %+v", c)
			}
			if got := b.open(t, c.WebhookURL); got != secretURL || strings.Contains(string(c.WebhookURL.Data), "SECRET") {
				t.Fatalf("URL not sealed to the daemon key: %q", got)
			}

			// Edit adds a header name and value to a channel stored without auth.
			d.press(kE, kTab, kTab)
			d.typ("X-Token")
			d.press(kTab)
			d.typ("v4lue")
			d.save()
			c = b.channel(t, "h1")
			if c.WebhookAuthName != "X-Token" || b.open(t, c.WebhookAuth) != "v4lue" || strings.Contains(string(c.WebhookAuth.Data), "v4lue") {
				t.Fatalf("add auth stored %+v", c)
			}
			if b.open(t, c.WebhookURL) != secretURL {
				t.Fatal("adding auth changed the URL")
			}

			// Keep.
			d.press(kE)
			d.save()
			if b.open(t, b.channel(t, "h1").WebhookAuth) != "v4lue" {
				t.Fatal("keep lost the auth value")
			}
			// Replace.
			d.press(kE, kTab, kTab, kTab)
			d.typ("n3w")
			d.save()
			if b.open(t, b.channel(t, "h1").WebhookAuth) != "n3w" {
				t.Fatal("replace did not store the new value")
			}
			// Clear keeps the header name.
			d.press(kE, kTab, kTab, kTab, kRight, kRight)
			d.save()
			if c = b.channel(t, "h1"); !c.WebhookAuth.Empty() || c.WebhookAuthName != "X-Token" || c.WebhookURL.Empty() {
				t.Fatalf("clear stored %+v", c)
			}

			// A new URL re-derives the origin and is sealed.
			d.press(kE, kTab)
			d.typ("http://other.example.org:8080/NEWPATH")
			d.save()
			c = b.channel(t, "h1")
			if c.WebhookOrigin != "http://other.example.org:8080" || b.open(t, c.WebhookURL) != "http://other.example.org:8080/NEWPATH" || strings.Contains(string(c.WebhookURL.Data), "NEWPATH") {
				t.Fatalf("URL replace stored %+v", c)
			}

			// The URL offers keep and replace only; replace with nothing typed is refused.
			d.press(kE, kTab, kRight, kRight)
			if out := d.screen(); strings.Contains(out, "‹ clear ›") {
				t.Fatalf("the URL offers clear:\n%s", out)
			}
			d.press(kRight)
			if out := d.rejects(); !strings.Contains(out, "URL is required") {
				t.Fatalf("empty replacement URL not refused:\n%s", out)
			}
			if b.open(t, b.channel(t, "h1").WebhookURL) != "http://other.example.org:8080/NEWPATH" {
				t.Fatal("a refused save changed the URL")
			}
			d.press(kEsc)

			// Typing a value and erasing it again stores nothing and is not an error.
			d.press(kE, kTab, kTab, kTab)
			d.typ("abc")
			d.back(3)
			if out := d.save(); strings.Contains(out, "empty") || strings.Contains(out, "Edit channel") {
				t.Fatalf("typed-then-erased value:\n%s", out)
			}
			if c = b.channel(t, "h1"); !c.WebhookAuth.Empty() {
				t.Fatalf("typed-then-erased value stored %+v", c)
			}
			// The same on a new channel with no header name.
			d.press(kC, kN)
			d.typ("h2")
			d.press(kTab, kRight, kTab)
			d.typ("https://x.example.com/p")
			d.press(kTab, kTab)
			d.typ("zz")
			d.back(2)
			if out := d.save(); strings.Contains(out, "empty") || strings.Contains(out, "New channel") {
				t.Fatalf("new channel with an erased value:\n%s", out)
			}
			if c = b.channel(t, "h2"); !c.WebhookAuth.Empty() || c.WebhookAuthName != "" {
				t.Fatalf("erased value stored %+v", c)
			}
		})
	}
}
