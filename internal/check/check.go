// Package check runs a single HTTP check.
package check

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

// Status is the outcome of one check run.
type Status string

const (
	StatusUp   Status = "up"
	StatusDown Status = "down"
	// StatusCanceled means the caller's context ended before the check
	// finished, for example on shutdown. It says nothing about the service
	// and must not be stored.
	StatusCanceled Status = "canceled"
)

// userAgent identifies the monitor to the checked services.
const userAgent = "up-monitor"

// maxDrain bounds how much of a response body is read before closing it, so
// the connection can be reused without downloading large pages.
const maxDrain = 64 << 10

// CertInfo describes the certificate served by the final HTTP response.
type CertInfo struct {
	Host, Issuer, Serial string
	NotAfter             time.Time
}

// Result is the outcome of one check run.
type Result struct {
	CheckName string
	At        time.Time
	Status    Status
	// Latency is the time until the response headers arrived; 0 when no
	// response was received.
	Latency time.Duration
	// StatusCode is the final HTTP status after redirects; 0 when no
	// response was received.
	StatusCode int
	// Error is the reason a check is down; empty when it is up.
	Error string
	// Cert is the final response's TLS leaf certificate, if any.
	Cert *CertInfo
}

// Checker runs HTTP checks.
type Checker struct {
	Client *http.Client
	Now    func() time.Time
}

// New returns a Checker using its own HTTP client and the wall clock.
func New() *Checker {
	return &Checker{Client: &http.Client{}, Now: time.Now}
}

// Run performs one GET request for c. Redirects are followed and the final
// status is compared with c.ExpectedStatus.
func (ch *Checker) Run(ctx context.Context, c config.Check) Result {
	res := Result{CheckName: c.Name, At: ch.Now()}

	reqCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, c.URL, nil)
	if err != nil {
		res.Status, res.Error = StatusDown, err.Error()
		return res
	}
	req.Header.Set("User-Agent", userAgent)

	start := time.Now()
	resp, err := ch.Client.Do(req)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			res.Status = StatusCanceled
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			res.Status, res.Error = StatusDown, fmt.Sprintf("timeout after %s", c.Timeout)
		default:
			res.Status, res.Error = StatusDown, cause(err)
		}
		return res
	}
	res.Latency = time.Since(start)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
	resp.Body.Close()

	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		cert := resp.TLS.PeerCertificates[0]
		issuer := cert.Issuer.CommonName
		if issuer == "" && len(cert.Issuer.Organization) > 0 {
			issuer = cert.Issuer.Organization[0]
		}
		res.Cert = &CertInfo{Host: resp.Request.URL.Hostname(), Issuer: issuer, Serial: cert.SerialNumber.Text(16), NotAfter: cert.NotAfter}
	}
	res.StatusCode = resp.StatusCode
	if resp.StatusCode != c.ExpectedStatus {
		res.Status = StatusDown
		res.Error = fmt.Sprintf("unexpected status %d (expected %d)", resp.StatusCode, c.ExpectedStatus)
		return res
	}
	res.Status = StatusUp
	return res
}

func isTimeout(err error) bool {
	var ue *url.Error
	return errors.As(err, &ue) && ue.Timeout()
}

// cause drops the `Get "<url>": ` prefix the client adds, since the URL is
// already shown next to the check.
func cause(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}
