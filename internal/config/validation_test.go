package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

func TestValidateCheck(t *testing.T) {
	valid := config.Check{Name: "api", URL: "https://example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200}
	tests := []struct {
		name   string
		change func(*config.Check)
		want   string
	}{
		{"valid", func(*config.Check) {}, ""},
		{"name", func(c *config.Check) { c.Name = "" }, "name is required"},
		{"url", func(c *config.Check) { c.URL = "" }, "url is required"},
		{"scheme", func(c *config.Check) { c.URL = "ftp://example.com" }, "must use http or https"},
		{"host", func(c *config.Check) { c.URL = "https://" }, "must include a host"},
		{"malformed URL", func(c *config.Check) { c.URL = "http://%" }, "is not valid"},
		{"zero interval", func(c *config.Check) { c.Interval = 0 }, "interval must be greater than 0"},
		{"negative interval", func(c *config.Check) { c.Interval = -time.Second }, "interval must be greater than 0"},
		{"zero timeout", func(c *config.Check) { c.Timeout = 0 }, "timeout must be greater than 0"},
		{"negative timeout", func(c *config.Check) { c.Timeout = -time.Second }, "timeout must be greater than 0"},
		{"timeout above interval", func(c *config.Check) { c.Timeout = time.Minute }, "must not exceed interval"},
		{"equal durations", func(c *config.Check) { c.Timeout = c.Interval }, ""},
		{"status low", func(c *config.Check) { c.ExpectedStatus = 99 }, "must be between 100 and 599"},
		{"status high", func(c *config.Check) { c.ExpectedStatus = 600 }, "must be between 100 and 599"},
		{"status lower bound", func(c *config.Check) { c.ExpectedStatus = 100 }, ""},
		{"status upper bound", func(c *config.Check) { c.ExpectedStatus = 599 }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid
			tt.change(&c)
			before := c
			err := config.ValidateCheck(c)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateCheck() = %v, want %q", err, tt.want)
			}
			if c != before {
				t.Fatal("validation changed input")
			}
		})
	}
}
