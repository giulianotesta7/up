package config

import (
	"errors"
	"fmt"
	"time"
)

// Defaults are the values the new monitor and new maintenance window forms
// start from. They never change existing monitors, channels or windows.
type Defaults struct {
	// Zone is the IANA zone of new maintenance windows.
	Zone string
	// Channel receives the alerts of new monitors; empty means none.
	Channel        string
	Interval       time.Duration
	Timeout        time.Duration
	ExpectedStatus int
	AlertAfter     int
	RecoverAfter   int
}

// BuiltinDefaults are the values in force until defaults are saved: the
// monitor values up always used, no channel and the UTC zone.
func BuiltinDefaults() Defaults {
	return Defaults{
		Zone: "UTC", Interval: 30 * time.Second, Timeout: 5 * time.Second,
		ExpectedStatus: 200, AlertAfter: 1, RecoverAfter: 1,
	}
}

// ValidateDefaults applies the existing rules: the zone rule of maintenance
// windows and the interval, timeout, expected status and threshold rules of
// ValidateCheck. Unlike a check, the thresholds are never 0: they are 1 to
// MaxAlertThreshold. Whether Channel exists is the store's rule.
func ValidateDefaults(d Defaults) error {
	var errs []error
	if err := ValidateZone(d.Zone); err != nil {
		errs = append(errs, err)
	}
	// A throwaway valid check carries the monitor fields through the one
	// validator every writer shares.
	probe := Check{
		Name: "defaults", URL: "http://defaults.invalid", Interval: d.Interval,
		Timeout: d.Timeout, ExpectedStatus: d.ExpectedStatus,
		AlertAfter: d.AlertAfter, RecoverAfter: d.RecoverAfter,
	}
	if err := ValidateCheck(probe); err != nil {
		errs = append(errs, err)
	}
	if d.AlertAfter == 0 {
		errs = append(errs, fmt.Errorf("alert after %d must be between 1 and %d", d.AlertAfter, MaxAlertThreshold))
	}
	if d.RecoverAfter == 0 {
		errs = append(errs, fmt.Errorf("recover after %d must be between 1 and %d", d.RecoverAfter, MaxAlertThreshold))
	}
	return errors.Join(errs...)
}
