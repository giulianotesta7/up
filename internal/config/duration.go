package config

import (
	"strings"
	"time"
)

// ShortDuration formats d without trailing zero units, so it reads the way
// people type it: "1m" instead of "1m0s", "2h" instead of "2h0m0s", while
// "1m30s" and "10s" stay as they are. time.ParseDuration accepts the result.
func ShortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
