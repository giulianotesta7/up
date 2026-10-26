// Package config loads YAML and validates HTTP check definitions.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"go.yaml.in/yaml/v3"
)

// Check is one validated HTTP check.
type Check struct {
	Name           string
	URL            string
	Interval       time.Duration
	Timeout        time.Duration
	ExpectedStatus int
}

// Config is the validated up configuration.
type Config struct {
	Checks []Check
}

// rawConfig mirrors the YAML file. Durations stay strings and the status a
// pointer so that missing and malformed values get their own error messages.
type rawConfig struct {
	Checks []rawCheck `yaml:"checks"`
}

type rawCheck struct {
	Name           string `yaml:"name"`
	URL            string `yaml:"url"`
	Interval       string `yaml:"interval"`
	Timeout        string `yaml:"timeout"`
	ExpectedStatus *int   `yaml:"expected_status"`
}

// Load reads and validates the configuration file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse validates YAML configuration data. It reports every problem found,
// not only the first one.
func Parse(data []byte) (Config, error) {
	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("config: invalid YAML: %w", err)
	}

	if len(raw.Checks) == 0 {
		return Config{}, errors.New("config: at least one check is required")
	}

	return validateRawChecks(raw.Checks)
}

// ValidateCheck validates a programmatic definition using the YAML check rules.
// All fields are required; durations must be positive, timeout must not exceed
// interval, and the expected HTTP status must be between 100 and 599.
func ValidateCheck(c Check) error {
	_, errs := parseCheck(rawFromCheck(c))
	return errors.Join(errs...)
}

// ValidateChecks validates a batch, including unique names. An empty batch is
// valid for an optional import; Parse still requires at least one YAML check.
func ValidateChecks(checks []Check) error {
	raw := make([]rawCheck, len(checks))
	for i, c := range checks {
		raw[i] = rawFromCheck(c)
	}
	_, err := validateRawChecks(raw)
	return err
}

func rawFromCheck(c Check) rawCheck {
	return rawCheck{
		Name: c.Name, URL: c.URL, Interval: c.Interval.String(),
		Timeout: c.Timeout.String(), ExpectedStatus: &c.ExpectedStatus,
	}
}

func validateRawChecks(checks []rawCheck) (Config, error) {
	var errs []error
	cfg := Config{Checks: make([]Check, 0, len(checks))}
	firstUse := make(map[string]int)
	for i, rc := range checks {
		check, checkErrs := parseCheck(rc)
		label := fmt.Sprintf("checks[%d]", i)
		if rc.Name != "" {
			label = fmt.Sprintf("checks[%d] (%s)", i, rc.Name)
			if first, ok := firstUse[rc.Name]; ok {
				checkErrs = append(checkErrs, fmt.Errorf("name %q is already used by checks[%d]", rc.Name, first))
			} else {
				firstUse[rc.Name] = i
			}
		}
		for _, err := range checkErrs {
			errs = append(errs, fmt.Errorf("config: %s: %w", label, err))
		}
		cfg.Checks = append(cfg.Checks, check)
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
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
		errs = append(errs, errors.New("expected_status is required"))
	case *rc.ExpectedStatus < 100 || *rc.ExpectedStatus > 599:
		errs = append(errs, fmt.Errorf("expected_status %d must be between 100 and 599", *rc.ExpectedStatus))
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
