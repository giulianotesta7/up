package config_test

import (
	"strings"
	"testing"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

func webhookChannel() config.Channel {
	return config.Channel{Name: "hook", Kind: config.KindWebhook, WebhookOrigin: "https://hooks.example.com",
		WebhookURL: secret.Sealed{Key: []byte{1}, Data: []byte{2}}, WebhookURLEdit: config.EditReplace}
}

func TestValidateWebhookChannel(t *testing.T) {
	tests := []struct {
		name   string
		change func(*config.Channel)
		want   string
	}{
		{"valid", func(*config.Channel) {}, ""},
		{"valid with auth", func(c *config.Channel) {
			c.WebhookAuthName = "Authorization"
			c.WebhookAuth = secret.Sealed{Key: []byte{1}, Data: []byte{3}}
			c.WebhookAuthEdit = config.EditReplace
		}, ""},
		{"origin required", func(c *config.Channel) { c.WebhookOrigin = "" }, "webhook origin is required"},
		{"origin scheme", func(c *config.Channel) { c.WebhookOrigin = "ftp://x.example.com" }, `webhook origin "ftp://x.example.com" must be http or https with a host`},
		{"origin path", func(c *config.Channel) { c.WebhookOrigin = "https://x.example.com/path" }, `webhook origin "https://x.example.com/path" must be http or https with a host`},
		{"origin userinfo", func(c *config.Channel) { c.WebhookOrigin = "https://u:p@x.example.com" }, "must be http or https with a host"},
		{"url needed", func(c *config.Channel) { c.WebhookURL = secret.Sealed{}; c.WebhookURLEdit = config.EditReplace }, "webhook URL replace needs a value"},
		{"url clear", func(c *config.Channel) { c.WebhookURLEdit = config.EditClear }, "webhook URL cannot be cleared"},
		{"url edit unknown", func(c *config.Channel) { c.WebhookURLEdit = 9 }, "webhook URL has an unknown edit"},
		{"auth name bad", func(c *config.Channel) { c.WebhookAuthName = "Bad Name" }, `webhook auth header name "Bad Name" is not valid`},
		{"auth value without name", func(c *config.Channel) {
			c.WebhookAuth = secret.Sealed{Key: []byte{1}, Data: []byte{3}}
			c.WebhookAuthEdit = config.EditReplace
		}, "webhook auth value requires a header name"},
		{"auth replace empty", func(c *config.Channel) { c.WebhookAuthName = "X"; c.WebhookAuthEdit = config.EditReplace }, "webhook auth value replace needs a value"},
		{"smtp server forbidden", func(c *config.Channel) { c.SMTPServer = "mail" }, "SMTP server and recipients are only for email channels"},
		{"recipients forbidden", func(c *config.Channel) { c.Recipients = []string{"a@example.com"} }, "SMTP server and recipients are only for email channels"},
		{"name", func(c *config.Channel) { c.Name = "" }, "name is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := webhookChannel()
			tt.change(&c)
			err := config.ValidateChannel(c)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateChannel() = %v, want %q", err, tt.want)
			}
		})
	}
	email := config.Channel{Name: "m", Kind: config.KindEmail, SMTPServer: "s", Recipients: []string{"a@example.com"}, WebhookOrigin: "https://x"}
	if err := config.ValidateChannel(email); err == nil || !strings.Contains(err.Error(), "webhook fields are only for webhook channels") {
		t.Fatalf("email with webhook fields: %v", err)
	}
}

func TestWebhookOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://hooks.example.com/services/T0/B0/secret?x=1": "https://hooks.example.com",
		"http://localhost:8080/path":                          "http://localhost:8080",
		"HTTPS://Hooks.Example.com/a":                         "https://hooks.example.com",
	} {
		got, err := config.WebhookOrigin(in)
		if err != nil || got != want {
			t.Errorf("WebhookOrigin(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "ftp://x.example.com", "https://", "not a url", "https://u:p@x.example.com/"} {
		if got, err := config.WebhookOrigin(in); err == nil || strings.Contains(err.Error(), "p@") && strings.Contains(err.Error(), ":p") {
			t.Errorf("WebhookOrigin(%q) = %q, %v, want an error without credentials", in, got, err)
		}
	}
}

func TestChannelKindsOfferWebhook(t *testing.T) {
	if got := strings.Join(config.ChannelKinds, ","); got != "email,webhook" {
		t.Fatalf("ChannelKinds = %q", got)
	}
}
