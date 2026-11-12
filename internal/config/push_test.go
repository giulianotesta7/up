package config_test

import (
	"strings"
	"testing"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
)

var sealedToken = secret.Sealed{Key: []byte{1}, Data: []byte{4}}

func pushChannel(kind string) config.Channel {
	c := config.Channel{Name: "push", Kind: kind}
	switch kind {
	case config.KindNtfy:
		c.ServerURL, c.Topic, c.Priority = config.DefaultNtfyServer, "up-alerts", "high"
	case config.KindTelegram:
		c.ChatID, c.Token, c.TokenEdit = "-1001234567890", sealedToken, config.EditReplace
	case config.KindDiscord, config.KindSlack:
		c.WebhookOrigin, c.WebhookURL, c.WebhookURLEdit = "https://discord.com", sealedToken, config.EditReplace
	case config.KindGotify:
		c.ServerURL, c.Token, c.TokenEdit, c.Priority = "http://nas.lan:8080", sealedToken, config.EditReplace, "5"
	}
	return c
}

func TestChannelKindsOrder(t *testing.T) {
	if got := strings.Join(config.ChannelKinds, ","); got != "email,webhook,ntfy,telegram,discord,slack,gotify" {
		t.Fatalf("ChannelKinds = %q", got)
	}
	if got := strings.Join(config.NtfyPriorities, ","); got != "min,low,default,high,max" {
		t.Fatalf("NtfyPriorities = %q", got)
	}
}

func TestValidatePushChannels(t *testing.T) {
	tests := []struct {
		kind   string
		name   string
		change func(*config.Channel)
		want   string
	}{
		{config.KindNtfy, "valid", func(*config.Channel) {}, ""},
		{config.KindNtfy, "valid with token", func(c *config.Channel) { c.Token, c.TokenEdit = sealedToken, config.EditReplace }, ""},
		{config.KindNtfy, "token clear", func(c *config.Channel) { c.TokenEdit = config.EditClear }, ""},
		{config.KindNtfy, "empty priority is default", func(c *config.Channel) { c.Priority = "" }, ""},
		{config.KindNtfy, "plain http server", func(c *config.Channel) { c.ServerURL = "http://ntfy.lan" }, ""},
		{config.KindNtfy, "server under a path", func(c *config.Channel) { c.ServerURL = "https://example.com/ntfy" }, ""},
		{config.KindNtfy, "server required", func(c *config.Channel) { c.ServerURL = "" }, "ntfy server URL is required"},
		{config.KindNtfy, "server scheme", func(c *config.Channel) { c.ServerURL = "ftp://ntfy.sh" }, "ntfy server URL must be http or https with a host"},
		{config.KindNtfy, "server credentials", func(c *config.Channel) { c.ServerURL = "https://user:hunter2@ntfy.sh" }, "ntfy server URL must be http or https with a host and no credentials"},
		{config.KindNtfy, "server trailing slash", func(c *config.Channel) { c.ServerURL = "https://ntfy.sh/" }, "ntfy server URL must be http or https with a host"},
		{config.KindNtfy, "server query", func(c *config.Channel) { c.ServerURL = "https://ntfy.sh?x=1" }, "ntfy server URL must be http or https with a host"},
		{config.KindNtfy, "topic required", func(c *config.Channel) { c.Topic = "" }, "ntfy topic is required"},
		{config.KindNtfy, "topic chars", func(c *config.Channel) { c.Topic = "up/alerts" }, `ntfy topic "up/alerts" may only use letters, digits, - and _ (at most 64)`},
		{config.KindNtfy, "topic length", func(c *config.Channel) { c.Topic = strings.Repeat("a", 65) }, "may only use letters, digits, - and _ (at most 64)"},
		{config.KindNtfy, "priority", func(c *config.Channel) { c.Priority = "urgent" }, `ntfy priority "urgent" must be min, low, default, high or max`},
		{config.KindNtfy, "token replace empty", func(c *config.Channel) { c.TokenEdit = config.EditReplace }, "ntfy access token replace needs a value"},
		{config.KindNtfy, "chat forbidden", func(c *config.Channel) { c.ChatID = "1" }, "chat ID is only for telegram channels"},
		{config.KindNtfy, "webhook forbidden", func(c *config.Channel) { c.WebhookOrigin = "https://x.example.com" }, "webhook URL is only for webhook, discord and slack channels"},
		{config.KindNtfy, "smtp forbidden", func(c *config.Channel) { c.SMTPServer = "mail" }, "SMTP server and recipients are only for email channels"},

		{config.KindTelegram, "valid", func(*config.Channel) {}, ""},
		{config.KindTelegram, "numeric chat", func(c *config.Channel) { c.ChatID = "123456789" }, ""},
		{config.KindTelegram, "public channel", func(c *config.Channel) { c.ChatID = "@up_alerts" }, ""},
		{config.KindTelegram, "keep token", func(c *config.Channel) { c.Token, c.TokenEdit = secret.Sealed{}, config.EditKeep }, ""},
		{config.KindTelegram, "chat required", func(c *config.Channel) { c.ChatID = "" }, "telegram chat ID is required"},
		{config.KindTelegram, "chat format", func(c *config.Channel) { c.ChatID = "my chat" }, `telegram chat ID "my chat" must be a number like -1001234567890 or @name`},
		{config.KindTelegram, "short name", func(c *config.Channel) { c.ChatID = "@ab" }, "must be a number like -1001234567890 or @name"},
		{config.KindTelegram, "token clear", func(c *config.Channel) { c.TokenEdit = config.EditClear }, "telegram bot token cannot be cleared"},
		{config.KindTelegram, "token replace empty", func(c *config.Channel) { c.Token = secret.Sealed{} }, "telegram bot token replace needs a value"},
		{config.KindTelegram, "server forbidden", func(c *config.Channel) { c.ServerURL = "https://x.example.com" }, "server URL is only for ntfy and gotify channels"},
		{config.KindTelegram, "topic forbidden", func(c *config.Channel) { c.Topic = "t" }, "topic is only for ntfy channels"},
		{config.KindTelegram, "priority forbidden", func(c *config.Channel) { c.Priority = "high" }, "priority is only for ntfy and gotify channels"},

		{config.KindDiscord, "valid", func(*config.Channel) {}, ""},
		{config.KindDiscord, "origin required", func(c *config.Channel) { c.WebhookOrigin = "" }, "discord webhook origin is required"},
		{config.KindDiscord, "origin path", func(c *config.Channel) { c.WebhookOrigin = "https://discord.com/api" }, `discord webhook origin "https://discord.com/api" must be http or https with a host`},
		{config.KindDiscord, "url clear", func(c *config.Channel) { c.WebhookURLEdit = config.EditClear }, "discord webhook URL cannot be cleared"},
		{config.KindDiscord, "url replace empty", func(c *config.Channel) { c.WebhookURL = secret.Sealed{} }, "discord webhook URL replace needs a value"},
		{config.KindDiscord, "auth forbidden", func(c *config.Channel) { c.WebhookAuthName = "Authorization" }, "webhook auth header is only for webhook channels"},
		{config.KindDiscord, "token forbidden", func(c *config.Channel) { c.Token, c.TokenEdit = sealedToken, config.EditReplace }, "token is only for ntfy, telegram and gotify channels"},

		{config.KindSlack, "valid", func(*config.Channel) {}, ""},
		{config.KindSlack, "origin required", func(c *config.Channel) { c.WebhookOrigin = "" }, "slack webhook origin is required"},
		{config.KindSlack, "url clear", func(c *config.Channel) { c.WebhookURLEdit = config.EditClear }, "slack webhook URL cannot be cleared"},
		{config.KindSlack, "recipients forbidden", func(c *config.Channel) { c.Recipients = []string{"a@example.com"} }, "SMTP server and recipients are only for email channels"},

		{config.KindGotify, "valid", func(*config.Channel) {}, ""},
		{config.KindGotify, "https server", func(c *config.Channel) { c.ServerURL = "https://gotify.example.com" }, ""},
		{config.KindGotify, "priority zero", func(c *config.Channel) { c.Priority = "0" }, ""},
		{config.KindGotify, "priority ten", func(c *config.Channel) { c.Priority = "10" }, ""},
		{config.KindGotify, "empty priority is default", func(c *config.Channel) { c.Priority = "" }, ""},
		{config.KindGotify, "server required", func(c *config.Channel) { c.ServerURL = "" }, "gotify server URL is required"},
		{config.KindGotify, "server credentials", func(c *config.Channel) { c.ServerURL = "http://admin:hunter2@nas.lan" }, "gotify server URL must be http or https with a host and no credentials"},
		{config.KindGotify, "priority range", func(c *config.Channel) { c.Priority = "11" }, `gotify priority "11" must be a whole number from 0 to 10`},
		{config.KindGotify, "priority negative", func(c *config.Channel) { c.Priority = "-1" }, "must be a whole number from 0 to 10"},
		{config.KindGotify, "priority word", func(c *config.Channel) { c.Priority = "high" }, "must be a whole number from 0 to 10"},
		{config.KindGotify, "priority padded", func(c *config.Channel) { c.Priority = "05" }, "must be a whole number from 0 to 10"},
		{config.KindGotify, "token clear", func(c *config.Channel) { c.TokenEdit = config.EditClear }, "gotify app token cannot be cleared"},
		{config.KindGotify, "topic forbidden", func(c *config.Channel) { c.Topic = "x" }, "topic is only for ntfy channels"},
	}
	for _, tt := range tests {
		t.Run(tt.kind+" "+tt.name, func(t *testing.T) {
			c := pushChannel(tt.kind)
			tt.change(&c)
			err := config.ValidateChannel(c)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateChannel() = %v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("error echoes a credential: %v", err)
			}
		})
	}
}

// Email and webhook channels refuse the push fields.
func TestEmailAndWebhookRefusePushFields(t *testing.T) {
	email := config.Channel{Name: "m", Kind: config.KindEmail, SMTPServer: "s", Recipients: []string{"a@example.com"}, Topic: "x"}
	if err := config.ValidateChannel(email); err == nil || !strings.Contains(err.Error(), "topic is only for ntfy channels") {
		t.Fatalf("email with a topic: %v", err)
	}
	hook := webhookChannel()
	hook.Token, hook.TokenEdit = sealedToken, config.EditReplace
	if err := config.ValidateChannel(hook); err == nil || !strings.Contains(err.Error(), "token is only for ntfy, telegram and gotify channels") {
		t.Fatalf("webhook with a token: %v", err)
	}
	if err := config.ValidateChannel(config.Channel{Name: "x", Kind: "sms"}); err == nil || !strings.Contains(err.Error(), `kind "sms" is not supported (use email, webhook, ntfy, telegram, discord, slack or gotify)`) {
		t.Fatalf("unknown kind: %v", err)
	}
}

func TestServerURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://ntfy.sh":            "https://ntfy.sh",
		" https://ntfy.sh/ ":         "https://ntfy.sh",
		"HTTP://NAS.lan:8080//":      "http://nas.lan:8080",
		"https://example.com/ntfy/":  "https://example.com/ntfy",
		"https://Example.com/Gotify": "https://example.com/Gotify",
	} {
		got, err := config.ServerURL(in)
		if err != nil || got != want {
			t.Errorf("ServerURL(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "ntfy.sh", "ftp://ntfy.sh", "https://", "https://u:hunter2@ntfy.sh", "https://ntfy.sh?x=1", "https://ntfy.sh#top"} {
		if got, err := config.ServerURL(in); err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("ServerURL(%q) = %q, %v, want an error without credentials", in, got, err)
		}
	}
}

func TestValidTelegramToken(t *testing.T) {
	for _, ok := range []string{"123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw", "1:a_b-c"} {
		if !config.ValidTelegramToken(ok) {
			t.Errorf("ValidTelegramToken(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "123456789", ":abc", "abc:def", "123:abc/../x", "123:abc?x=1", "123:abc def"} {
		if config.ValidTelegramToken(bad) {
			t.Errorf("ValidTelegramToken(%q) = true", bad)
		}
	}
}

func TestSendsTo(t *testing.T) {
	for _, tt := range []struct {
		c    config.Channel
		want string
	}{
		{config.Channel{Kind: config.KindEmail, SMTPServer: "gmail", Recipients: []string{"ana@acme.com", "b@acme.com"}}, "gmail → ana@acme.com +1"},
		{config.Channel{Kind: config.KindEmail, SMTPServer: "gmail", Recipients: []string{"ana@acme.com"}}, "gmail → ana@acme.com"},
		{config.Channel{Kind: config.KindWebhook, WebhookOrigin: "https://hooks.acme.com", WebhookURL: sealedToken}, "https://hooks.acme.com"},
		{pushChannel(config.KindNtfy), "https://ntfy.sh · up-alerts"},
		{pushChannel(config.KindTelegram), "chat -1001234567890"},
		{pushChannel(config.KindDiscord), "https://discord.com"},
		{pushChannel(config.KindSlack), "https://discord.com"},
		{pushChannel(config.KindGotify), "http://nas.lan:8080"},
		{config.Channel{Kind: "sms"}, ""},
	} {
		if got := tt.c.SendsTo(); got != tt.want {
			t.Errorf("SendsTo(%s) = %q, want %q", tt.c.Kind, got, tt.want)
		}
	}
}
