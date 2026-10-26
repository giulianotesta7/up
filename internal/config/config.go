// Package config defines and validates HTTP check definitions.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"time"
)

// Check is one validated HTTP check.
type Check struct {
	Name           string
	URL            string
	Interval       time.Duration
	Timeout        time.Duration
	ExpectedStatus int
}

// rawCheck keeps durations as strings and the status as a pointer so that
// missing and malformed values get their own error messages.
type rawCheck struct {
	Name           string
	URL            string
	Interval       string
	Timeout        string
	ExpectedStatus *int
}

// ValidateCheck validates a check definition.
// All fields are required; durations must be positive, timeout must not exceed
// interval, and the expected HTTP status must be between 100 and 599.
func ValidateCheck(c Check) error {
	_, errs := parseCheck(rawFromCheck(c))
	return errors.Join(errs...)
}

func rawFromCheck(c Check) rawCheck {
	return rawCheck{
		Name: c.Name, URL: c.URL, Interval: c.Interval.String(),
		Timeout: c.Timeout.String(), ExpectedStatus: &c.ExpectedStatus,
	}
}

func parseCheck(rc rawCheck) (Check, []error) {
	var errs []error
	check := Check{Name: rc.Name, URL: rc.URL}

	if rc.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}

	if err := validateURL(rc.URL); err != nil {
		errs = append(errs, err)
	}

	interval, err := parsePositiveDuration("interval", rc.Interval)
	if err != nil {
		errs = append(errs, err)
	}
	check.Interval = interval

	timeout, err := parsePositiveDuration("timeout", rc.Timeout)
	if err != nil {
		errs = append(errs, err)
	}
	check.Timeout = timeout

	if interval > 0 && timeout > interval {
		errs = append(errs, fmt.Errorf("timeout (%s) must not exceed interval (%s)", rc.Timeout, rc.Interval))
	}

	switch {
	case rc.ExpectedStatus == nil:
		errs = append(errs, errors.New("expected status is required"))
	case *rc.ExpectedStatus < 100 || *rc.ExpectedStatus > 599:
		errs = append(errs, fmt.Errorf("expected status %d must be between 100 and 599", *rc.ExpectedStatus))
	default:
		check.ExpectedStatus = *rc.ExpectedStatus
	}

	return check, errs
}

func validateURL(raw string) error {
	if raw == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url %q is not valid: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url %q must use http or https", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q must include a host", raw)
	}
	return nil
}

func parsePositiveDuration(field, raw string) (time.Duration, error) {
	if raw == "" {
		return 0, fmt.Errorf("%s is required", field)
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a duration (use values like 30s or 1m)", field, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0", field)
	}
	return d, nil
}
