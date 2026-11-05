package alert

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

const (
	// WebhookTimeout bounds one webhook request, from dial to the end of the
	// (discarded) response. There are no retries.
	WebhookTimeout = 10 * time.Second
	// webhookReadCap is the most of a response body that is read; the body is
	// never kept or reported.
	webhookReadCap = 4 << 10
	// WebhookPayloadVersion is the "version" of the JSON body.
	WebhookPayloadVersion = 1
	webhookUserAgent      = "up/webhooks"
)

// webhookClient does not follow redirects: a 3xx is a failed delivery, so a
// webhook cannot bounce the request (and its auth header) elsewhere.
var webhookClient = &http.Client{
	Timeout:       WebhookTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

type webhookMonitor struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type webhookResult struct {
	Status        string `json:"status,omitempty"`
	Reason        string `json:"reason,omitempty"`
	StatusCode    int    `json:"status_code"`
	LatencyMS     int64  `json:"latency_ms"`
	DaysRemaining *int   `json:"days_remaining,omitempty"`
}

type webhookPayload struct {
	Version    int             `json:"version"`
	Event      string          `json:"event"`
	OccurredAt string          `json:"occurred_at"`
	Channel    string          `json:"channel,omitempty"`
	Message    string          `json:"message,omitempty"`
	Monitor    *webhookMonitor `json:"monitor,omitempty"`
	Result     *webhookResult  `json:"result,omitempty"`
}

func webhookBody(e event, now time.Time) webhookPayload {
	if e.test {
		return webhookPayload{Version: WebhookPayloadVersion, Event: "test", OccurredAt: now.UTC().Format(time.RFC3339), Channel: e.check.Channel,
			Message: "This is a test notification from up; no monitor is involved."}
	}
	kind := e.check.Kind
	if kind == "" {
		kind = config.CheckHTTP
	}
	p := webhookPayload{Version: WebhookPayloadVersion, Monitor: &webhookMonitor{Name: e.check.Name, Type: kind}}
	if e.cert != nil {
		days := int(e.remaining / (24 * time.Hour))
		p.Event, p.OccurredAt = "cert.expiring", e.cert.Observed.UTC().Format(time.RFC3339)
		p.Result = &webhookResult{DaysRemaining: &days}
		return p
	}
	r := e.result
	p.Event, p.OccurredAt = "monitor.recovered", r.At.UTC().Format(time.RFC3339)
	if r.Status == check.StatusDown {
		p.Event = "monitor.down"
	}
	p.Result = &webhookResult{Status: string(r.Status), Reason: r.Error, StatusCode: r.StatusCode, LatencyMS: r.Latency.Milliseconds()}
	return p
}

// webhookFailure turns a transport error into text without the URL, which
// url.Error would include.
func webhookFailure(err error) string {
	var dns *net.DNSError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var host x509.HostnameError
	var record tls.RecordHeaderError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	case errors.Is(err, context.Canceled):
		return "request canceled"
	case errors.As(err, &dns):
		return "host name could not be resolved"
	case errors.As(err, &unknown), errors.As(err, &invalid), errors.As(err, &host):
		return "TLS certificate was not accepted"
	case errors.As(err, &record):
		return "TLS handshake failed"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "request timed out"
	}
	return "connection failed"
}

// deliverWebhook is the webhook transport behind Deliver. It POSTs the event
// as JSON, treats a 2xx answer as delivered and everything else as an error.
// It logs its own failures and returns them, like deliverEmail; errors carry
// no URL, auth value or response body.
func (n *Notifier) deliverWebhook(ctx context.Context, ch config.Channel, e event) error {
	fail := func(msg string) error {
		n.opts.Logger.Error("webhook delivery failed", "channel", e.check.Channel, "error", msg)
		return errors.New(msg)
	}
	if n.opts.Key == nil {
		return fail("webhook cannot be opened: no encryption key")
	}
	target, err := n.opts.Key.Open(ch.WebhookURL)
	switch {
	case errors.Is(err, secret.ErrWrongKey):
		return fail("webhook URL was sealed to another key; enter it again")
	case err != nil:
		return fail("opening the webhook URL failed")
	case target == "":
		return fail("webhook has no URL")
	}
	var auth string
	if ch.WebhookAuthName != "" {
		if auth, err = n.opts.Key.Open(ch.WebhookAuth); errors.Is(err, secret.ErrWrongKey) {
			return fail("webhook auth value was sealed to another key; enter it again")
		} else if err != nil {
			return fail("opening the webhook auth value failed")
		}
	}
	body, err := json.Marshal(webhookBody(e, time.Now()))
	if err != nil {
		return fail("encoding the webhook payload failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fail("webhook URL is not valid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", webhookUserAgent)
	if ch.WebhookAuthName != "" && auth != "" {
		req.Header.Set(ch.WebhookAuthName, auth)
	}
	resp, err := webhookClient.Do(req)
	if err != nil {
		return fail("webhook " + webhookFailure(err))
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, webhookReadCap)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fail(fmt.Sprintf("webhook answered %d %s", resp.StatusCode, http.StatusText(resp.StatusCode)))
	}
	n.opts.Logger.Info("alert sent", "check", e.check.Name, "channel", e.check.Channel, "event", webhookBody(e, time.Now()).Event)
	return nil
}
