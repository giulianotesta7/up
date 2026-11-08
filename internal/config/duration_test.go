package config_test

import (
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

func TestShortDurationDropsTrailingZeroUnits(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{10 * time.Second, "10s"},
		{time.Minute, "1m"},
		{90 * time.Second, "1m30s"},
		{time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
		{time.Hour + 30*time.Second, "1h0m30s"},
		{config.MaxHeartbeatPeriod, "744h"},
		{0, "0s"},
		{1500 * time.Millisecond, "1.5s"},
	} {
		got := config.ShortDuration(tc.in)
		if got != tc.want {
			t.Errorf("ShortDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
		if back, err := time.ParseDuration(got); err != nil || back != tc.in {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", got, back, err, tc.in)
		}
	}
}
