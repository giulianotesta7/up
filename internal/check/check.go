// Package check runs a single HTTP or TCP check.
package check

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
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
	// Dial opens TCP check connections; nil dials directly.
	Dial DialFunc
	// Key opens the sealed request headers and body; nil when the daemon has
	// none, in which case checks with protected values are down.
	Key *secret.Key
}

// New returns a Checker using its own HTTP client and the wall clock.
func New() *Checker {
	return &Checker{Client: &http.Client{}, Now: time.Now}
}

// Run performs one check for c: a TCP connect for Kind tcp, otherwise one
// request (GET unless it sets a method). Redirects are followed and the final
// status is compared with c.ExpectedStatus, then the expected text and the
// conditions are checked in order; the first that fails is the reason.
func (ch *Checker) Run(ctx context.Context, c config.Check) Result {
	if c.Kind == config.CheckTCP {
		return ch.runTCP(ctx, c)
	}
	res := Result{CheckName: c.Name, At: ch.Now()}

	reqCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	req, err := ch.newRequest(reqCtx, c)
	if err != nil {
		res.Status, res.Error = StatusDown, err.Error()
		return res
	}

	start := time.Now()
	resp, err := ch.client(c).Do(req)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			res.Status = StatusCanceled
		case errors.Is(err, errRedirectLeavesOrigin):
			res.Status, res.Error = StatusDown, errRedirectLeavesOrigin.Error()
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			res.Status, res.Error = StatusDown, fmt.Sprintf("timeout after %s", c.Timeout)
		default:
			res.Status, res.Error = StatusDown, cause(err)
		}
		return res
	}
	res.Latency = time.Since(start)
	defer resp.Body.Close()

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
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
		res.Status = StatusDown
		res.Error = fmt.Sprintf("unexpected status %d (expected %d)", resp.StatusCode, c.ExpectedStatus)
		return res
	}
	if !c.ReadsBody() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
		res.Status, res.Error = StatusUp, failedCondition(c.Conditions, &response{latency: res.Latency, header: resp.Header})
		if res.Error != "" {
			res.Status = StatusDown
		}
		return res
	}
	body, truncated, err := readBody(resp.Body)
	switch {
	case err != nil && ctx.Err() != nil:
		res.Status = StatusCanceled
	case err != nil && (errors.Is(err, context.DeadlineExceeded) || isTimeout(err)):
		res.Status, res.Error = StatusDown, fmt.Sprintf("timeout after %s", c.Timeout)
	case err != nil:
		res.Status, res.Error = StatusDown, err.Error()
	case c.ExpectText != "" && !bytes.Contains(body, []byte(c.ExpectText)):
		res.Status, res.Error = StatusDown, textFailure(truncated)
	default:
		res.Status = StatusUp
		r := response{latency: res.Latency, header: resp.Header, body: body, truncated: truncated}
		if res.Error = failedCondition(c.Conditions, &r); res.Error != "" {
			res.Status = StatusDown
		}
	}
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
