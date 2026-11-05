package tui

import (
	"testing"
	"time"
)

func TestOutageDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                       "0s",
		400 * time.Millisecond:                  "0s",
		45*time.Second + 900*time.Millisecond:   "45s",
		time.Minute:                             "1m",
		time.Minute + 33*time.Second + 79171432: "1m 33s",
		28 * time.Minute:                        "28m",
		3*time.Hour + 12*time.Minute + 5*time.Second: "3h 12m",
		3 * time.Hour:  "3h",
		50 * time.Hour: "2d 2h",
	} {
		if got := outageDuration(d); got != want {
			t.Errorf("outageDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
