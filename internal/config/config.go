// Package config defines and validates check definitions.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/heohlee/up/internal/secret"
)

// Cert is the last observed TLS certificate and its notification state.
type Cert struct {
	Check, Host, Issuer, Serial string
	NotAfter, Observed          time.Time
	Notified                    int
}

// CertWarnDays lists certificate expiry warning thresholds.
var CertWarnDays = []int{14, 7, 3}

// Mute records when alert delivery resumes and the recipients' prior state.
type Mute struct {
	Check       string
	Until       time.Time
	WasAlerting bool
}

// Check is one validated check, HTTP (the default), TCP, heartbeat, ping or
// DNS.
//
// A Check holds a slice, so it is not comparable with ==; use SameDefinition.
// Build one from external input (API, form) and call Canonical before storing
// or comparing it; the store does so on every write.
type Check struct {
	Name string
	// Kind is CheckHTTP, CheckTCP, CheckHeartbeat, CheckPing or CheckDNS;
	// empty means HTTP.
	Kind           string
	URL            string
	Interval       time.Duration
	Timeout        time.Duration
	ExpectedStatus int
	// Method is the HTTP method; empty means GET. HTTP checks only.
	Method string
	// Headers are sent in order. Names are plain; values are sealed (see
	// Header). HTTP checks only.
	Headers []Header
	// Body is the sealed request body and BodyEdit says how a write treats
	// it (see Edit). HTTP checks only.
	Body     secret.Sealed
	BodyEdit Edit
	// ExpectText is a literal, case-sensitive text the response body must
	// contain. HTTP checks only.
	ExpectText string
	// Conditions are extra rules the response must meet, in order (see
	// Condition). Empty means none. HTTP checks only.
	Conditions []Condition
	// Host and Port are the TCP target. Host has no brackets for IPv6.
	// TCP checks only, except that ping checks have a Host and no Port.
	Host string
	Port int
	// Domain is the name a DNS check queries and Record its type, one of
	// DNSRecords. Resolver is the server, "host", "host:port" or
	// "[ipv6]:port" (port 53 when absent; see ResolverAddress), or empty for
	// the system resolver. Expected, when set, is the value one of the
	// answers must have: an address for A and AAAA, the target host for
	// CNAME and MX (case and a final dot do not matter; the MX priority is
	// ignored) and the whole text for TXT (a record's strings joined). DNS
	// checks only.
	Domain   string
	Record   string
	Resolver string
	Expected string
	// Channel is the name of the channel that receives this check's alerts;
	// empty means no alerts.
	Channel string
	// AlertAfter is how many failed checks in a row send the down alert, and
	// RecoverAfter how many successful ones send the recovery alert. Zero
	// means 1; use FailuresToAlert and SuccessesToRecover.
	AlertAfter   int
	RecoverAfter int
	// TLSOff turns off certificate monitoring for an HTTPS check: no
	// expiry warnings and no stored certificate. The zero value keeps it on.
	TLSOff bool
	// Period is how often a heartbeat's job pings and Grace how late a ping
	// may still arrive: the check is down when no ping arrives within
	// Period + Grace of the last one. Heartbeat checks only, which have no
	// URL, method, expected status, interval or timeout.
	Period time.Duration
	Grace  time.Duration
}

// MaxAlertThreshold is the largest AlertAfter and RecoverAfter.
const MaxAlertThreshold = 10

// FailuresToAlert is AlertAfter with the default applied.
func (c Check) FailuresToAlert() int { return max(c.AlertAfter, 1) }

// SuccessesToRecover is RecoverAfter with the default applied.
func (c Check) SuccessesToRecover() int { return max(c.RecoverAfter, 1) }

// rawCheck keeps durations as strings and the status as a pointer so that
// missing and malformed values get their own error messages.
type rawCheck struct {
	Name           string
	URL            string
	Interval       string
	Timeout        string
	ExpectedStatus *int
	// NotHTTP skips the URL and status rules, which only apply to HTTP.
	NotHTTP bool
}

// ValidateCheck validates a check definition and is the single validation
// entry point for every writer. For HTTP all of name, URL, interval, timeout
// and expected status are required; durations must be positive, timeout must
// not exceed interval, and the expected status must be between 100 and 599.
// Ping and DNS checks share the name, interval and timeout rules. See
// validateHTTPFields, validateTCPFields, validatePingFields and
// validateDNSFields for the rest.
func ValidateCheck(c Check) error {
	var errs []error
	switch c.Kind {
	case "", CheckHTTP, CheckTCP, CheckPing, CheckDNS:
	case CheckHeartbeat:
		errs = validateHeartbeatFields(c)
	default:
		errs = append(errs, fmt.Errorf("kind %q must be http, tcp, heartbeat, ping or dns", c.Kind))
	}
	if c.Kind != CheckDNS && c.Kind != CheckPing && c.hasDNSFields() {
		errs = append(errs, errDNSFields)
	}
	switch {
	case c.Kind == CheckHeartbeat:
	case c.Period != 0 || c.Grace != 0:
		errs = append(errs, errors.New("period and grace are only for heartbeat checks"))
		fallthrough
	default:
		errs = append(errs, validateProbe(c)...)
	}
	if c.AlertAfter < 0 || c.AlertAfter > MaxAlertThreshold {
		errs = append(errs, fmt.Errorf("alert after %d must be between 1 and %d", c.AlertAfter, MaxAlertThreshold))
	}
	if c.RecoverAfter < 0 || c.RecoverAfter > MaxAlertThreshold {
		errs = append(errs, fmt.Errorf("recover after %d must be between 1 and %d", c.RecoverAfter, MaxAlertThreshold))
	}
	return errors.Join(errs...)
}

// validateProbe checks the fields of a check the daemon runs itself: HTTP,
// TCP, ping and DNS.
func validateProbe(c Check) []error {
	var errs []error
	raw := rawFromCheck(c)
	raw.NotHTTP = c.Kind == CheckTCP || c.Kind == CheckPing || c.Kind == CheckDNS
	_, parsed := parseCheck(raw)
	errs = append(errs, parsed...)
	switch c.Kind {
	case CheckTCP:
		errs = append(errs, validateTCPFields(c)...)
	case CheckPing:
		errs = append(errs, validatePingFields(c)...)
	case CheckDNS:
		errs = append(errs, validateDNSFields(c)...)
	default:
		errs = append(errs, validateHTTPFields(c)...)
	}
	return errs
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

	if !rc.NotHTTP {
		if err := validateURL(rc.URL); err != nil {
			errs = append(errs, err)
		}
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
	case rc.NotHTTP:
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
