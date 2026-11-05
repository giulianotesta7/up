package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

func TestBuiltinDefaultsAreTodaysValues(t *testing.T) {
	want := config.Defaults{Zone: "UTC", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200, AlertAfter: 1, RecoverAfter: 1}
	if got := config.BuiltinDefaults(); got != want {
		t.Fatalf("BuiltinDefaults() = %+v, want %+v", got, want)
	}
	if err := config.ValidateDefaults(want); err != nil {
		t.Fatalf("built-in defaults invalid: %v", err)
	}
}

func TestValidateDefaults(t *testing.T) {
	tests := []struct {
		name   string
		change func(*config.Defaults)
		want   string
	}{
		{"valid", func(*config.Defaults) {}, ""},
		{"channel is not a validator rule", func(d *config.Defaults) { d.Channel = "ops" }, ""},
		{"zone empty", func(d *config.Defaults) { d.Zone = "" }, "zone is required"},
		{"zone local", func(d *config.Defaults) { d.Zone = "Local" }, `zone "Local" is not an IANA time zone`},
		{"zone unknown", func(d *config.Defaults) { d.Zone = "Mars/Base" }, `zone "Mars/Base" is not an IANA time zone`},
		{"zone iana", func(d *config.Defaults) { d.Zone = "America/Argentina/Buenos_Aires" }, ""},
		{"zero interval", func(d *config.Defaults) { d.Interval = 0 }, "interval must be greater than 0"},
		{"zero timeout", func(d *config.Defaults) { d.Timeout = 0 }, "timeout must be greater than 0"},
		{"timeout above interval", func(d *config.Defaults) { d.Timeout, d.Interval = 10*time.Second, 5*time.Second }, "timeout (10s) must not exceed interval (5s)"},
		{"status low", func(d *config.Defaults) { d.ExpectedStatus = 99 }, "expected status 99 must be between 100 and 599"},
		{"status high", func(d *config.Defaults) { d.ExpectedStatus = 600 }, "expected status 600 must be between 100 and 599"},
		{"alert zero", func(d *config.Defaults) { d.AlertAfter = 0 }, "alert after 0 must be between 1 and 10"},
		{"alert high", func(d *config.Defaults) { d.AlertAfter = 11 }, "alert after 11 must be between 1 and 10"},
		{"alert max", func(d *config.Defaults) { d.AlertAfter = 10 }, ""},
		{"recover zero", func(d *config.Defaults) { d.RecoverAfter = 0 }, "recover after 0 must be between 1 and 10"},
		{"recover high", func(d *config.Defaults) { d.RecoverAfter = 11 }, "recover after 11 must be between 1 and 10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := config.BuiltinDefaults()
			tt.change(&d)
			err := config.ValidateDefaults(d)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("error = %v, want none", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
