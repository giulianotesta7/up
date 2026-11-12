package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

func heartbeatCheck() config.Check {
	return config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: 24 * time.Hour, Grace: 30 * time.Minute}
}

func TestValidateHeartbeat(t *testing.T) {
	tests := []struct {
		name   string
		change func(*config.Check)
		want   string
	}{
		{"valid", func(*config.Check) {}, ""},
		{"thresholds and channel are shared", func(c *config.Check) { c.AlertAfter, c.RecoverAfter = 2, 3 }, ""},
		{"shortest period", func(c *config.Check) { c.Period = time.Minute }, ""},
		{"longest period", func(c *config.Check) { c.Period = config.MaxHeartbeatPeriod }, ""},
		{"no grace", func(c *config.Check) { c.Grace = 0 }, ""},
		{"longest grace", func(c *config.Check) { c.Grace = config.MaxHeartbeatGrace }, ""},
		{"period missing", func(c *config.Check) { c.Period = 0 }, "period must be between 1m and 744h"},
		{"period too short", func(c *config.Check) { c.Period = 59 * time.Second }, "period must be between 1m and 744h"},
		{"period too long", func(c *config.Check) { c.Period = config.MaxHeartbeatPeriod + time.Second }, "period must be between 1m and 744h"},
		{"grace negative", func(c *config.Check) { c.Grace = -time.Second }, "grace must be between 0s and 744h"},
		{"grace too long", func(c *config.Check) { c.Grace = config.MaxHeartbeatGrace + time.Second }, "grace must be between 0s and 744h"},
		{"name", func(c *config.Check) { c.Name = "" }, "name is required"},
		{"url", func(c *config.Check) { c.URL = "https://example.com" }, "url, method, expected status, timeout and interval are not used by heartbeat checks"},
		{"interval", func(c *config.Check) { c.Interval = time.Minute }, "are not used by heartbeat checks"},
		{"timeout", func(c *config.Check) { c.Timeout = time.Second }, "are not used by heartbeat checks"},
		{"status", func(c *config.Check) { c.ExpectedStatus = 200 }, "are not used by heartbeat checks"},
		{"method", func(c *config.Check) { c.Method = "POST" }, "are not used by heartbeat checks"},
		{"headers", func(c *config.Check) { c.Headers = []config.Header{{Name: "X"}} }, "headers, body, expected text, host, port and tls are not used by heartbeat checks"},
		{"body", func(c *config.Check) { c.Body = sealed(1) }, "headers, body, expected text, host, port and tls are not used"},
		{"expect", func(c *config.Check) { c.ExpectText = "ok" }, "headers, body, expected text, host, port and tls are not used"},
		{"host", func(c *config.Check) { c.Host = "db" }, "headers, body, expected text, host, port and tls are not used"},
		{"port", func(c *config.Check) { c.Port = 1 }, "headers, body, expected text, host, port and tls are not used"},
		{"tls off", func(c *config.Check) { c.TLSOff = true }, "headers, body, expected text, host, port and tls are not used"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := heartbeatCheck()
			tt.change(&c)
			err := config.ValidateCheck(c)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateCheck() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestPeriodAndGraceAreOnlyForHeartbeats(t *testing.T) {
	for _, base := range []func() config.Check{httpCheck, tcpCheck} {
		for _, change := range []func(*config.Check){
			func(c *config.Check) { c.Period = time.Hour },
			func(c *config.Check) { c.Grace = time.Minute },
		} {
			c := base()
			change(&c)
			if err := config.ValidateCheck(c); err == nil || !strings.Contains(err.Error(), "period and grace are only for heartbeat checks") {
				t.Fatalf("ValidateCheck(%+v) = %v", c, err)
			}
		}
	}
}

func TestHeartbeatDefinitionIdentity(t *testing.T) {
	base := heartbeatCheck()
	for name, change := range map[string]func(*config.Check){
		"period": func(c *config.Check) { c.Period = time.Hour },
		"grace":  func(c *config.Check) { c.Grace = time.Hour },
	} {
		other := base
		change(&other)
		if base.SameDefinition(other) {
			t.Errorf("changing %s kept the same definition", name)
		}
	}
	if got := base.Canonical(); got.Kind != config.CheckHeartbeat {
		t.Fatalf("Canonical() kind = %q", got.Kind)
	}
	if got := base.Target(); got != "" {
		t.Fatalf("Target() = %q, want none for a heartbeat", got)
	}
}

func TestAPIListenerBaseURL(t *testing.T) {
	tests := []struct {
		api  config.APIListener
		host string
		want string
	}{
		{config.APIListener{}, "box", ""},
		{config.APIListener{Address: "10.0.0.5:8080"}, "box", "http://10.0.0.5:8080"},
		{config.APIListener{Address: "up.example.com:443", TLS: true}, "box", "https://up.example.com:443"},
		{config.APIListener{Address: "[::]:8080"}, "box", "http://box:8080"},
		{config.APIListener{Address: "0.0.0.0:8080", TLS: true}, "box", "https://box:8080"},
		{config.APIListener{Address: ":8080"}, "", "http://localhost:8080"},
		{config.APIListener{Address: "[2001:db8::1]:8080"}, "box", "http://[2001:db8::1]:8080"},
	}
	for _, tt := range tests {
		if got := tt.api.BaseURL(tt.host); got != tt.want {
			t.Errorf("%+v.BaseURL(%q) = %q, want %q", tt.api, tt.host, got, tt.want)
		}
		if tt.api.Enabled() != (tt.want != "") {
			t.Errorf("%+v.Enabled() = %v", tt.api, tt.api.Enabled())
		}
	}
	if got := config.HeartbeatPath("uphb_x") + config.HeartbeatFailSuffix; got != "/api/v1/heartbeat/uphb_x/fail" {
		t.Fatalf("fail path = %q", got)
	}
}
