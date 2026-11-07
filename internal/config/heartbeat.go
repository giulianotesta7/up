package config

import (
	"errors"
	"fmt"
	"net"
	"time"
)

// CheckHeartbeat is the kind of a check the daemon does not run: a job pings
// up when it finishes, and the check is down when no ping arrives in time.
const CheckHeartbeat = "heartbeat"

// Bounds on a heartbeat's period and grace. The longest period covers a
// monthly job (31 days); a grace may be as long, so a late job never needs a
// period it does not have.
const (
	MinHeartbeatPeriod = time.Minute
	MaxHeartbeatPeriod = 31 * 24 * time.Hour
	MaxHeartbeatGrace  = MaxHeartbeatPeriod
)

// Values the new heartbeat form starts from: a daily job with an hour of slack.
const (
	DefaultHeartbeatPeriod = 24 * time.Hour
	DefaultHeartbeatGrace  = time.Hour
)

// validateHeartbeatFields checks a heartbeat's name, period and grace and
// that no field of a check the daemon runs is set.
func validateHeartbeatFields(c Check) []error {
	var errs []error
	if c.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if c.Period < MinHeartbeatPeriod || c.Period > MaxHeartbeatPeriod {
		errs = append(errs, fmt.Errorf("period must be between %s and %s", ShortDuration(MinHeartbeatPeriod), ShortDuration(MaxHeartbeatPeriod)))
	}
	if c.Grace < 0 || c.Grace > MaxHeartbeatGrace {
		errs = append(errs, fmt.Errorf("grace must be between 0s and %s", ShortDuration(MaxHeartbeatGrace)))
	}
	if c.URL != "" || c.Method != "" || c.ExpectedStatus != 0 || c.Timeout != 0 || c.Interval != 0 {
		errs = append(errs, errors.New("url, method, expected status, timeout and interval are not used by heartbeat checks"))
	}
	if len(c.Headers) > 0 || !c.Body.Empty() || c.BodyEdit != EditKeep || c.ExpectText != "" || c.Host != "" || c.Port != 0 || c.TLSOff {
		errs = append(errs, errors.New("headers, body, expected text, host, port and tls are not used by heartbeat checks"))
	}
	return errs
}

// HeartbeatPath is the API path a heartbeat's job POSTs to; add
// HeartbeatFailSuffix when the job failed.
func HeartbeatPath(token string) string { return "/api/v1/heartbeat/" + token }

// HeartbeatFailSuffix turns a ping URL into the one that reports a failure.
const HeartbeatFailSuffix = "/fail"

// APIListener is the address the daemon's API listens on (`up serve
// --api-listen`, as bound) and whether it serves HTTPS. The zero value means
// the daemon runs without an API, so heartbeat pings cannot arrive.
type APIListener struct {
	Address string
	TLS     bool
}

// Enabled reports whether the daemon serves the API.
func (a APIListener) Enabled() bool { return a.Address != "" }

// BaseURL is the scheme and host pings use, without a trailing slash, or ""
// without an API. An unspecified host (":8080", "0.0.0.0:8080",
// "[::]:8080") becomes host, or localhost when host is empty; behind a proxy
// the user replaces it.
func (a APIListener) BaseURL(host string) string {
	if !a.Enabled() {
		return ""
	}
	scheme := "http"
	if a.TLS {
		scheme = "https"
	}
	h, port, err := net.SplitHostPort(a.Address)
	if err != nil {
		return scheme + "://" + a.Address
	}
	if ip := net.ParseIP(h); h == "" || (ip != nil && ip.IsUnspecified()) {
		h = host
		if h == "" {
			h = "localhost"
		}
	}
	return scheme + "://" + net.JoinHostPort(h, port)
}
