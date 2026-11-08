package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
)

func pushChannel(name, kind string) config.Channel {
	c := config.Channel{Name: name, Kind: kind}
	switch kind {
	case config.KindNtfy:
		c.ServerURL, c.Topic, c.Priority, c.Token, c.TokenEdit = config.DefaultNtfyServer, "up-alerts", "high", seal(3, 3), config.EditReplace
	case config.KindTelegram:
		c.ChatID, c.Token, c.TokenEdit = "@up_alerts", seal(4, 4), config.EditReplace
	case config.KindDiscord, config.KindSlack:
		c.WebhookOrigin, c.WebhookURL, c.WebhookURLEdit = "https://discord.com", seal(5, 5), config.EditReplace
	case config.KindGotify:
		c.ServerURL, c.Priority, c.Token, c.TokenEdit = "http://nas.lan:8080", "8", seal(6, 6), config.EditReplace
	}
	return c
}

var pushKinds = []string{config.KindNtfy, config.KindTelegram, config.KindDiscord, config.KindSlack, config.KindGotify}

func TestPushChannelsRoundTrip(t *testing.T) {
	s, path := seeded(t)
	for _, kind := range pushKinds {
		if err := s.AddChannel(ctx, pushChannel(kind, kind)); err != nil {
			t.Fatalf("AddChannel(%s) = %v", kind, err)
		}
	}
	all, err := s.Channels(ctx)
	if err != nil || len(all) != len(pushKinds) {
		t.Fatalf("Channels() = %+v, %v", all, err)
	}
	for _, got := range all {
		want := pushChannel(got.Kind, got.Kind)
		want.TokenEdit, want.WebhookURLEdit = config.EditKeep, config.EditKeep
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip %s:\n got %+v\nwant %+v", got.Kind, got, want)
		}
	}
	// Reopening keeps every field.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	again, err := ro.Channels(ctx)
	if err != nil || !reflect.DeepEqual(again, all) {
		t.Fatalf("after reopen = %+v, %v", again, err)
	}
}

func TestPushTokenKeepReplaceClear(t *testing.T) {
	s, path := seeded(t)
	if err := s.AddChannel(ctx, pushChannel("phone", config.KindNtfy)); err != nil {
		t.Fatal(err)
	}
	update := func(edit func(*config.Channel)) config.Channel {
		t.Helper()
		c := storedChannel(t, s, "phone")
		edit(&c)
		if err := s.UpdateChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
		return storedChannel(t, s, "phone")
	}
	// Keep retains the stored token even when the caller holds other bytes.
	got := update(func(c *config.Channel) { c.Token, c.Topic, c.Priority = seal(9), "other", "max" })
	if !reflect.DeepEqual(got.Token, seal(3, 3)) || got.Topic != "other" || got.Priority != "max" {
		t.Fatalf("keep: %+v", got)
	}
	got = update(func(c *config.Channel) { c.Token, c.TokenEdit = seal(7), config.EditReplace })
	if !reflect.DeepEqual(got.Token, seal(7)) {
		t.Fatalf("replace: %+v", got)
	}
	got = update(func(c *config.Channel) { c.TokenEdit = config.EditClear })
	if !got.Token.Empty() || got.Topic != "other" {
		t.Fatalf("clear: %+v", got)
	}
	// An ntfy channel works without a token.
	noToken := pushChannel("open", config.KindNtfy)
	noToken.Token, noToken.TokenEdit = secret.Sealed{}, config.EditKeep
	if err := s.AddChannel(ctx, noToken); err != nil {
		t.Fatal(err)
	}

	// Telegram and gotify tokens and discord and slack URLs are required and
	// cannot be cleared; rejected writes change nothing.
	for _, kind := range []string{config.KindTelegram, config.KindGotify, config.KindDiscord, config.KindSlack} {
		if err := s.AddChannel(ctx, pushChannel(kind, kind)); err != nil {
			t.Fatal(err)
		}
	}
	before := diskState(t, path)
	for _, tt := range []struct {
		kind, want string
		edit       func(*config.Channel)
	}{
		{config.KindTelegram, "telegram bot token cannot be cleared", func(c *config.Channel) { c.TokenEdit = config.EditClear }},
		{config.KindGotify, "gotify app token cannot be cleared", func(c *config.Channel) { c.TokenEdit = config.EditClear }},
		{config.KindDiscord, "discord webhook URL cannot be cleared", func(c *config.Channel) { c.WebhookURLEdit = config.EditClear }},
		{config.KindSlack, "slack webhook URL cannot be cleared", func(c *config.Channel) { c.WebhookURLEdit = config.EditClear }},
	} {
		c := storedChannel(t, s, tt.kind)
		tt.edit(&c)
		if err := s.UpdateChannel(ctx, c); !errors.Is(err, store.ErrValidation) || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%s: UpdateChannel = %v, want %q", tt.kind, err, tt.want)
		}
	}
	for _, tt := range []struct{ kind, want string }{
		{config.KindTelegram, "telegram bot token is required"},
		{config.KindGotify, "gotify app token is required"},
		{config.KindDiscord, "discord webhook URL is required"},
		{config.KindSlack, "slack webhook URL is required"},
	} {
		c := pushChannel("new-"+tt.kind, tt.kind)
		c.Token, c.TokenEdit, c.WebhookURL, c.WebhookURLEdit = secret.Sealed{}, config.EditKeep, secret.Sealed{}, config.EditKeep
		if err := s.AddChannel(ctx, c); !errors.Is(err, store.ErrValidation) || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%s without secret: AddChannel = %v, want %q", tt.kind, err, tt.want)
		}
	}
	if !reflect.DeepEqual(diskState(t, path), before) {
		t.Fatal("rejected writes changed the database")
	}
}

// A kept secret belongs to its kind: switching kinds needs a new one, and
// switching to email drops every push field.
func TestPushKindSwitchKeepsNoOtherKindsSecret(t *testing.T) {
	s, _ := seeded(t)
	if err := s.AddChannel(ctx, pushChannel("tg", config.KindTelegram)); err != nil {
		t.Fatal(err)
	}
	to := pushChannel("tg", config.KindGotify)
	to.Token, to.TokenEdit = secret.Sealed{}, config.EditKeep
	if err := s.UpdateChannel(ctx, to); !errors.Is(err, store.ErrValidation) || !strings.Contains(err.Error(), "gotify app token is required") {
		t.Fatalf("keep a telegram token as a gotify one = %v", err)
	}
	if err := s.AddChannel(ctx, webhook("hook")); err != nil {
		t.Fatal(err)
	}
	slack := pushChannel("hook", config.KindSlack)
	slack.WebhookURL, slack.WebhookURLEdit = secret.Sealed{}, config.EditKeep
	if err := s.UpdateChannel(ctx, slack); !errors.Is(err, store.ErrValidation) || !strings.Contains(err.Error(), "slack webhook URL is required") {
		t.Fatalf("keep a webhook URL as a slack one = %v", err)
	}
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "smtp", Host: "h", Port: 25, TLS: config.TLSNone, Sender: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	email := config.Channel{Name: "tg", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}
	if err := s.UpdateChannel(ctx, email); err != nil {
		t.Fatal(err)
	}
	if got := storedChannel(t, s, "tg"); !reflect.DeepEqual(got, email) {
		t.Fatalf("channel back to email = %+v", got)
	}
}
