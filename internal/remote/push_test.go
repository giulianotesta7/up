package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/tui"
)

func TestPushChannelRoutes(t *testing.T) {
	h, s := foundationServer(t)
	ctx := context.Background()
	for _, tt := range []struct {
		body string
		want ChannelDTO
	}{
		{`{"name":"phone","kind":"ntfy","server_url":"https://ntfy.sh","topic":"up-alerts","priority":"high","token":` + replaceJSON(1) + `}`,
			ChannelDTO{Name: "phone", Kind: "ntfy", ServerURL: "https://ntfy.sh", Topic: "up-alerts", Priority: "high", TokenSet: true}},
		{`{"name":"tg","kind":"telegram","chat_id":"-1001234567890","token":` + replaceJSON(2) + `}`,
			ChannelDTO{Name: "tg", Kind: "telegram", ChatID: "-1001234567890", TokenSet: true}},
		{`{"name":"dc","kind":"discord","webhook_origin":"https://discord.com","webhook_url":` + replaceJSON(3) + `}`,
			ChannelDTO{Name: "dc", Kind: "discord", WebhookOrigin: "https://discord.com", WebhookURLSet: true}},
		{`{"name":"sl","kind":"slack","webhook_origin":"https://hooks.slack.com","webhook_url":` + replaceJSON(4) + `}`,
			ChannelDTO{Name: "sl", Kind: "slack", WebhookOrigin: "https://hooks.slack.com", WebhookURLSet: true}},
		{`{"name":"nas","kind":"gotify","server_url":"http://nas.lan:8080","priority":"8","token":` + replaceJSON(5) + `}`,
			ChannelDTO{Name: "nas", Kind: "gotify", ServerURL: "http://nas.lan:8080", Priority: "8", TokenSet: true}},
	} {
		rec := send(h, "POST", "/api/v1/channels", tt.body)
		if rec.Code != 201 {
			t.Fatalf("create %s: %d %s", tt.want.Kind, rec.Code, rec.Body)
		}
		for _, leaked := range []string{`"sealed"`, `"data"`} {
			if strings.Contains(rec.Body.String(), leaked) {
				t.Fatalf("response leaks %q: %s", leaked, rec.Body)
			}
		}
		var dto ChannelDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil || !reflect.DeepEqual(dto, tt.want) {
			t.Fatalf("dto = %+v, %v, want %+v", dto, err, tt.want)
		}
	}
	if ch, _, _ := s.Channel(ctx, "tg"); !reflect.DeepEqual(ch.Token.Data, []byte{2}) {
		t.Fatalf("stored telegram = %+v", ch)
	}
	// Absent keeps the token, clear clears the ntfy one.
	if rec := send(h, "PUT", "/api/v1/channels/phone", `{"kind":"ntfy","server_url":"https://ntfy.sh","topic":"other","priority":"max"}`); rec.Code != 204 {
		t.Fatalf("keep: %d %s", rec.Code, rec.Body)
	}
	if ch, _, _ := s.Channel(ctx, "phone"); !reflect.DeepEqual(ch.Token.Data, []byte{1}) || ch.Topic != "other" || ch.Priority != "max" {
		t.Fatalf("after keep = %+v", ch)
	}
	if rec := send(h, "PUT", "/api/v1/channels/phone", `{"kind":"ntfy","server_url":"https://ntfy.sh","topic":"other","token":{"action":"clear"}}`); rec.Code != 204 {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if ch, _, _ := s.Channel(ctx, "phone"); !ch.Token.Empty() {
		t.Fatalf("after clear = %+v", ch)
	}
	before, _ := s.Channels(ctx)
	for label, tt := range map[string]struct{ path, body, want string }{
		"telegram clear":   {"tg", `{"kind":"telegram","chat_id":"-1","token":{"action":"clear"}}`, "telegram bot token cannot be cleared"},
		"gotify no token":  {"new", `{"kind":"gotify","server_url":"http://nas.lan"}`, "gotify app token is required"},
		"ntfy topic":       {"phone", `{"kind":"ntfy","server_url":"https://ntfy.sh","topic":"a/b"}`, "ntfy topic"},
		"server creds":     {"nas", `{"kind":"gotify","server_url":"http://u:hunter2@nas.lan"}`, "gotify server URL must be http or https"},
		"other key":        {"tg", `{"kind":"telegram","chat_id":"-1","token":{"action":"replace","sealed":` + sealedJSON(bytes.Repeat([]byte{1}, 32), 1) + `}}`, "token was sealed to a different key"},
		"token on discord": {"dc", `{"kind":"discord","webhook_origin":"https://discord.com","token":` + replaceJSON(6) + `}`, "token is only for ntfy, telegram and gotify channels"},
	} {
		method := "PUT"
		if tt.path == "new" {
			method = "POST"
			tt.body = strings.Replace(tt.body, "{", `{"name":"new",`, 1)
			tt.path = ""
		} else {
			tt.path = "/" + tt.path
		}
		rec := send(h, method, "/api/v1/channels"+tt.path, tt.body)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), tt.want) || strings.Contains(rec.Body.String(), "hunter2") {
			t.Errorf("%s: %d %s, want 400 with %q", label, rec.Code, rec.Body, tt.want)
		}
	}
	if after, _ := s.Channels(ctx); !reflect.DeepEqual(after, before) {
		t.Fatal("rejected writes changed the channels")
	}
	if rec := send(h, "DELETE", "/api/v1/channels/nas", ""); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, ok, _ := s.Channel(ctx, "nas"); ok {
		t.Fatal("gotify channel not deleted")
	}
	if rec := send(h, "POST", "/api/v1/channels/tg/test", ""); rec.Code != 202 {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
}

func TestSnapshotCarriesPushChannelsWithoutSecrets(t *testing.T) {
	now := time.Date(2026, 3, 2, 14, 30, 0, 0, time.UTC)
	sealed := func(b byte) secret.Sealed { return secret.Sealed{Key: serverKey, Data: bytes.Repeat([]byte{b}, 40)} }
	snap := tui.Snapshot{PublicKey: serverKey, HasKey: true, Channels: []config.Channel{
		{Name: "phone", Kind: config.KindNtfy, ServerURL: "https://ntfy.sh", Topic: "up-alerts", Priority: "high", Token: sealed(1)},
		{Name: "open", Kind: config.KindNtfy, ServerURL: "https://ntfy.sh", Topic: "public"},
		{Name: "tg", Kind: config.KindTelegram, ChatID: "@up_alerts", Token: sealed(2)},
		{Name: "dc", Kind: config.KindDiscord, WebhookOrigin: "https://discord.com", WebhookURL: sealed(3)},
		{Name: "nas", Kind: config.KindGotify, ServerURL: "http://nas.lan:8080", Priority: "8", Token: sealed(4)},
	}}
	raw, err := json.Marshal(NewSnapshotDTO(snap, now))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []byte{1, 2, 3, 4} {
		v := bytes.Repeat([]byte{b}, 40)
		if bytes.Contains(raw, v) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(v))) {
			t.Fatalf("secret %d leaked: %s", b, raw)
		}
	}
	for _, want := range []string{`"server_url":"https://ntfy.sh"`, `"topic":"up-alerts"`, `"priority":"high"`, `"token_set":true`, `"token_set":false`, `"chat_id":"@up_alerts"`, `"kind":"gotify"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("snapshot lacks %s:\n%s", want, raw)
		}
	}
	var dto SnapshotDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		t.Fatal(err)
	}
	got := dto.Snapshot().Channels
	if len(got) != len(snap.Channels) {
		t.Fatalf("channels = %+v", got)
	}
	for i, c := range got {
		want := snap.Channels[i]
		if c.Kind != want.Kind || c.ServerURL != want.ServerURL || c.Topic != want.Topic || c.ChatID != want.ChatID || c.Priority != want.Priority ||
			c.Token.Empty() != want.Token.Empty() || c.WebhookURL.Empty() != want.WebhookURL.Empty() || c.SendsTo() != want.SendsTo() {
			t.Errorf("channel %d = %+v, want %+v", i, c, want)
		}
		if !c.Token.Empty() && bytes.Equal(c.Token.Data, want.Token.Data) {
			t.Errorf("channel %d carries the token", i)
		}
	}
}
