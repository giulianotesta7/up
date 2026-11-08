package client

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

// Every push kind travels client -> API -> store -> snapshot -> client, its
// secret sealed on the way in and only "set" on the way back.
func TestPushChannelsThroughTheClient(t *testing.T) {
	c, s, key := realServer(t)
	ctx := context.Background()
	channels := []config.Channel{
		{Name: "phone", Kind: config.KindNtfy, ServerURL: config.DefaultNtfyServer, Topic: "up-alerts", Priority: "high", Token: sealTo(t, key, "tk_SECRET"), TokenEdit: config.EditReplace},
		{Name: "tg", Kind: config.KindTelegram, ChatID: "-1001234567890", Token: sealTo(t, key, "123:SECRET"), TokenEdit: config.EditReplace},
		{Name: "dc", Kind: config.KindDiscord, WebhookOrigin: "https://discord.com", WebhookURL: sealTo(t, key, "https://discord.com/api/webhooks/1/SECRET"), WebhookURLEdit: config.EditReplace},
		{Name: "sl", Kind: config.KindSlack, WebhookOrigin: "https://hooks.slack.com", WebhookURL: sealTo(t, key, "https://hooks.slack.com/services/SECRET"), WebhookURLEdit: config.EditReplace},
		{Name: "nas", Kind: config.KindGotify, ServerURL: "http://nas.lan:8080", Priority: "8", Token: sealTo(t, key, "ASECRET"), TokenEdit: config.EditReplace},
	}
	for _, ch := range channels {
		if err := c.AddChannel(ctx, ch); err != nil {
			t.Fatalf("AddChannel(%s) = %v", ch.Kind, err)
		}
	}
	snap, err := c.LoadSnapshot(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]config.Channel{}
	for _, ch := range snap.Channels {
		byName[ch.Name] = ch
	}
	for _, want := range channels {
		got := byName[want.Name]
		if got.Kind != want.Kind || got.SendsTo() != want.SendsTo() || got.Priority != want.Priority || got.Token.Empty() != want.Token.Empty() || got.WebhookURL.Empty() != want.WebhookURL.Empty() {
			t.Errorf("snapshot %s = %+v", want.Name, got)
		}
		if strings.Contains(string(got.Token.Data)+string(got.WebhookURL.Data), "SECRET") {
			t.Errorf("snapshot %s carries a secret", want.Name)
		}
	}
	stored, _, _ := s.Channel(ctx, "tg")
	if tok, err := key.Open(stored.Token); err != nil || tok != "123:SECRET" {
		t.Fatalf("stored token = %q, %v", tok, err)
	}

	// Edit from the snapshot's channel: keep is the default, clear clears.
	phone := byName["phone"]
	phone.Topic = "other"
	if err := c.UpdateChannel(ctx, phone); err != nil {
		t.Fatal(err)
	}
	if stored, _, _ = s.Channel(ctx, "phone"); stored.Topic != "other" || stored.Token.Empty() {
		t.Fatalf("after keep = %+v", stored)
	}
	phone.TokenEdit = config.EditClear
	if err := c.UpdateChannel(ctx, phone); err != nil {
		t.Fatal(err)
	}
	if stored, _, _ = s.Channel(ctx, "phone"); !stored.Token.Empty() {
		t.Fatalf("after clear = %+v", stored)
	}
	tg := byName["tg"]
	tg.Token, tg.TokenEdit = sealTo(t, key, "456:NEW"), config.EditReplace
	if err := c.UpdateChannel(ctx, tg); err != nil {
		t.Fatal(err)
	}
	if stored, _, _ = s.Channel(ctx, "tg"); func() string { v, _ := key.Open(stored.Token); return v }() != "456:NEW" {
		t.Fatal("telegram token not replaced")
	}
	// A rejection reaches the caller.
	bad := byName["nas"]
	bad.Priority = "11"
	if err := c.UpdateChannel(ctx, bad); err == nil || !strings.Contains(err.Error(), `gotify priority "11" must be a whole number from 0 to 10`) {
		t.Fatalf("invalid update = %v", err)
	}
	if _, err := c.EnqueueChannelTest(ctx, "nas", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteChannel(ctx, "sl"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Channel(ctx, "sl"); ok {
		t.Fatal("slack channel not deleted")
	}
}

// A raw API caller gets the TUI's server URL form and token check.
func TestPushChannelValidationThroughTheClient(t *testing.T) {
	c, s, key := realServer(t)
	ctx := context.Background()
	if err := c.AddChannel(ctx, config.Channel{Name: "phone", Kind: config.KindNtfy, ServerURL: "HTTPS://NTFY.SH/", Topic: "up"}); err != nil {
		t.Fatalf("AddChannel = %v", err)
	}
	if stored, _, _ := s.Channel(ctx, "phone"); stored.ServerURL != "https://ntfy.sh" {
		t.Fatalf("stored server = %q", stored.ServerURL)
	}
	if err := c.AddChannel(ctx, config.Channel{Name: "creds", Kind: config.KindNtfy, ServerURL: "https://u:hunter2@ntfy.sh/", Topic: "up"}); err == nil ||
		!strings.Contains(err.Error(), "ntfy server URL must be http or https") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("credential URL = %v", err)
	}
	tg := config.Channel{Name: "tg", Kind: config.KindTelegram, ChatID: "-1", Token: sealTo(t, key, "123:OK"), TokenEdit: config.EditReplace}
	if err := c.AddChannel(ctx, tg); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Channels(ctx)
	bad := tg
	bad.Name, bad.Token = "bad", sealTo(t, key, "notatoken")
	if err := c.AddChannel(ctx, bad); err == nil || !strings.Contains(err.Error(), "bot token must look like 123456:ABC-def") || strings.Contains(err.Error(), "notatoken") {
		t.Fatalf("bad create = %v", err)
	}
	tg.Token = sealTo(t, key, "notatoken")
	if err := c.UpdateChannel(ctx, tg); err == nil || !strings.Contains(err.Error(), "bot token must look like 123456:ABC-def") {
		t.Fatalf("bad replace = %v", err)
	}
	if after, _ := s.Channels(ctx); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected writes changed the channels")
	}
}
