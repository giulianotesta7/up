package alert

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

type hookRequest struct {
	method, path, query, contentType, agent, auth string
	body                                          map[string]any
}

// hookNotifier returns a notifier whose "hook" channel posts to a test server
// answering with status, plus what the server saw.
func hookNotifier(t *testing.T, status int, authName, authValue string, handler http.HandlerFunc) (*Notifier, *httptest.Server, chan hookRequest) {
	t.Helper()
	got := make(chan hookRequest, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got <- hookRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type"), r.Header.Get("User-Agent"), r.Header.Get(authName), body}
		if handler != nil {
			handler(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	key, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
	if err != nil {
		t.Fatal(err)
	}
	target := srv.URL + "/hooks/SECRETPATH?token=SECRETQUERY"
	sealedURL, err := secret.Seal(key.PublicKey(), target)
	if err != nil {
		t.Fatal(err)
	}
	ch := config.Channel{Kind: config.KindWebhook, WebhookOrigin: srv.URL, WebhookURL: sealedURL, WebhookAuthName: authName}
	if authValue != "" {
		if ch.WebhookAuth, err = secret.Seal(key.PublicKey(), authValue); err != nil {
			t.Fatal(err)
		}
	}
	st := customStore{found: true, channel: ch}
	return NewNotifier(Options{Store: st, Key: key, Logger: quiet(), Location: time.UTC}), srv, got
}

func TestWebhookPostsVersionedPayload(t *testing.T) {
	n, _, got := hookNotifier(t, 204, "Authorization", "Bearer s3cret", nil)
	c := config.Check{Name: "api", URL: "https://example.com", Channel: "hook"}
	r := check.Result{Status: check.StatusDown, At: deliverAt, Error: "status 500", StatusCode: 500, Latency: 1500 * time.Millisecond}
	if err := n.Deliver(context.Background(), c, r); err != nil {
		t.Fatal(err)
	}
	h := <-got
	if h.method != "POST" || h.path != "/hooks/SECRETPATH" || h.query != "token=SECRETQUERY" || h.contentType != "application/json" {
		t.Fatalf("request %+v", h)
	}
	if !strings.HasPrefix(h.agent, "up/") || h.auth != "Bearer s3cret" {
		t.Fatalf("agent %q auth %q", h.agent, h.auth)
	}
	want := map[string]any{
		"version": float64(1), "event": "monitor.down", "occurred_at": "2025-01-02T18:04:05Z",
		"monitor": map[string]any{"name": "api", "type": "http"},
		"result":  map[string]any{"status": "down", "reason": "status 500", "status_code": float64(500), "latency_ms": float64(1500)},
	}
	a, _ := json.Marshal(h.body)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Fatalf("payload\n got %s\nwant %s", a, b)
	}

	r = check.Result{Status: check.StatusUp, At: deliverAt, StatusCode: 200, Latency: 20 * time.Millisecond}
	if err := n.Deliver(context.Background(), config.Check{Name: "db", Kind: config.CheckTCP, Channel: "hook"}, r); err != nil {
		t.Fatal(err)
	}
	h = <-got
	if h.body["event"] != "monitor.recovered" || h.body["monitor"].(map[string]any)["type"] != "tcp" {
		t.Fatalf("recovered %+v", h.body)
	}
}

func TestWebhookCertAndTestEvents(t *testing.T) {
	n, _, got := hookNotifier(t, 200, "", "", nil)
	c := config.Check{Name: "api", Channel: "hook"}
	cert := config.Cert{Check: "api", Observed: deliverAt, NotAfter: deliverAt.Add(120 * time.Hour)}
	if err := n.DeliverCert(context.Background(), c, cert, 5*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	h := <-got
	res := h.body["result"].(map[string]any)
	if h.body["event"] != "cert.expiring" || res["days_remaining"] != float64(5) || h.body["occurred_at"] != "2025-01-02T18:04:05Z" {
		t.Fatalf("cert payload %+v", h.body)
	}
	if h.auth != "" {
		t.Fatalf("auth header sent without configuration: %q", h.auth)
	}

	err := n.deliver(context.Background(), event{test: true, check: config.Check{Channel: "hook"}})
	if err != nil {
		t.Fatal(err)
	}
	h = <-got
	if h.body["event"] != "test" || h.body["version"] != float64(1) {
		t.Fatalf("test payload %+v", h.body)
	}
	if h.body["channel"] != "hook" {
		t.Fatalf("test event channel = %v", h.body["channel"])
	}
	if _, ok := h.body["result"]; ok {
		t.Fatalf("test event has a result: %+v", h.body)
	}
	if _, ok := h.body["monitor"]; ok {
		t.Fatalf("test event has a monitor: %+v", h.body)
	}
	if msg, _ := h.body["message"].(string); !strings.Contains(msg, "test") {
		t.Fatalf("test event does not say it is a test: %+v", h.body)
	}
}

func TestWebhookErrorsAreSanitized(t *testing.T) {
	c := config.Check{Name: "api", Channel: "hook"}
	r := check.Result{Status: check.StatusDown, At: deliverAt}
	for _, status := range []int{301, 404, 500} {
		redirects := int32(0)
		n, srv, _ := hookNotifier(t, status, "Authorization", "AUTHVALUE", func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == "/elsewhere" {
				atomic.AddInt32(&redirects, 1)
			}
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "RESPONSEBODY")
		})
		err := n.Deliver(context.Background(), c, r)
		if err == nil {
			t.Fatalf("status %d: no error", status)
		}
		for _, leak := range []string{"SECRETPATH", "SECRETQUERY", "AUTHVALUE", "RESPONSEBODY", srv.URL} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("status %d: error leaks %q: %v", status, leak, err)
			}
		}
		if !strings.Contains(err.Error(), http.StatusText(status)) {
			t.Fatalf("status %d: error lacks the status line: %v", status, err)
		}
		if atomic.LoadInt32(&redirects) != 0 {
			t.Fatalf("status %d: redirect followed", status)
		}
	}
}

func TestWebhookNetworkErrorAndTimeoutAreSanitized(t *testing.T) {
	n, srv, _ := hookNotifier(t, 200, "Authorization", "AUTHVALUE", nil)
	srv.Close()
	err := n.Deliver(context.Background(), config.Check{Name: "api", Channel: "hook"}, check.Result{Status: check.StatusDown, At: deliverAt})
	if err == nil {
		t.Fatal("no error for closed server")
	}
	for _, leak := range []string{"SECRETPATH", "SECRETQUERY", "AUTHVALUE", "127.0.0.1"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks %q: %v", leak, err)
		}
	}

	n, _, _ = hookNotifier(t, 200, "", "", func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = n.Deliver(ctx, config.Check{Name: "api", Channel: "hook"}, check.Result{Status: check.StatusDown, At: deliverAt})
	if err == nil || strings.Contains(err.Error(), "SECRETPATH") || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestWebhookWithoutKeyOrWrongKeyFails(t *testing.T) {
	n, _, _ := hookNotifier(t, 200, "", "", nil)
	n.opts.Key = nil
	c := config.Check{Name: "api", Channel: "hook"}
	if err := n.Deliver(context.Background(), c, check.Result{Status: check.StatusDown, At: deliverAt}); err == nil || strings.Contains(err.Error(), "SECRETPATH") {
		t.Fatalf("no key: %v", err)
	}
	other, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
	if err != nil {
		t.Fatal(err)
	}
	n.opts.Key = other
	if err := n.Deliver(context.Background(), c, check.Result{Status: check.StatusDown, At: deliverAt}); err == nil || !strings.Contains(err.Error(), "another key") {
		t.Fatalf("wrong key: %v", err)
	}
}

// Email channels still go through SMTP, unchanged, once webhooks exist.
func TestEmailChannelUnchangedByWebhookTransport(t *testing.T) {
	sender := notifierSender{make(chan Message, 1)}
	n := NewNotifier(Options{Store: emailStore(), Sender: sender, Hostname: "host", Location: time.UTC, Logger: quiet()})
	c := config.Check{Name: "api", URL: "https://example.com", Channel: "mail"}
	if err := n.Deliver(context.Background(), c, check.Result{Status: check.StatusDown, At: deliverAt, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	m := <-sender.messages
	if m.Subject != "[up] api is DOWN" || !strings.Contains(m.Body, "Error:  boom") || len(m.To) != 1 || m.To[0] != "a@example.com" {
		t.Fatalf("email changed: %+v", m)
	}
}
