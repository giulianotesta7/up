package alert

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
)

// Secrets of the push channels under test. None may reach an error, a log
// line or a recorded reason.
const (
	ntfyToken     = "tk_NTFYSECRET"
	telegramToken = "123456:TGSECRETtoken"
	gotifyToken   = "AGOTIFYSECRET"
	discordPath   = "/api/webhooks/1/DISCORDSECRET"
	slackPath     = "/services/T0/B0/SLACKSECRET"
)

var pushSecrets = []string{"NTFYSECRET", "TGSECRET", "GOTIFYSECRET", "DISCORDSECRET", "SLACKSECRET", "Bearer"}

type pushRequest struct {
	method, path string
	header       http.Header
	body         []byte
}

func (r pushRequest) json(t *testing.T) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(r.body, &v); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	return v
}

// text is what a person reads in the request: title and message together.
func (r pushRequest) text(t *testing.T, kind string) string {
	t.Helper()
	switch kind {
	case config.KindNtfy:
		return r.header.Get("Title") + "\n" + string(r.body)
	case config.KindTelegram:
		return r.json(t)["text"].(string)
	case config.KindDiscord:
		return r.json(t)["content"].(string)
	case config.KindSlack:
		return r.json(t)["text"].(string)
	case config.KindGotify:
		b := r.json(t)
		return b["title"].(string) + "\n" + b["message"].(string)
	}
	t.Fatalf("unknown kind %q", kind)
	return ""
}

type pushSetup struct {
	n   *Notifier
	srv *httptest.Server
	got chan pushRequest
	ch  config.Channel
}

// pushNotifier is a notifier whose channel "push" of the given kind delivers
// to a test server (also telegram's API) that answers through handler, or
// 200 when it is nil. change adjusts the channel before its secrets are
// sealed.
func pushNotifier(t *testing.T, kind string, change func(*config.Channel), handler http.HandlerFunc) pushSetup {
	t.Helper()
	got := make(chan pushRequest, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- pushRequest{r.Method, r.URL.Path, r.Header.Clone(), body}
		if handler != nil {
			handler(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	key, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
	if err != nil {
		t.Fatal(err)
	}
	seal := func(plain string) secret.Sealed {
		v, err := secret.Seal(key.PublicKey(), plain)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	ch := config.Channel{Name: "push", Kind: kind}
	switch kind {
	case config.KindNtfy:
		ch.ServerURL, ch.Topic, ch.Priority, ch.Token = srv.URL, "up-alerts", "high", seal(ntfyToken)
	case config.KindTelegram:
		ch.ChatID, ch.Token = "-1001234567890", seal(telegramToken)
	case config.KindDiscord:
		ch.WebhookOrigin, ch.WebhookURL = srv.URL, seal(srv.URL+discordPath)
	case config.KindSlack:
		ch.WebhookOrigin, ch.WebhookURL = srv.URL, seal(srv.URL+slackPath)
	case config.KindGotify:
		ch.ServerURL, ch.Priority, ch.Token = srv.URL, "8", seal(gotifyToken)
	}
	if change != nil {
		change(&ch)
	}
	n := NewNotifier(Options{Store: customStore{found: true, channel: ch}, Key: key, Logger: quiet(), Location: time.UTC, Hostname: "host", TelegramAPI: srv.URL})
	return pushSetup{n, srv, got, ch}
}

var pushKinds = []string{config.KindNtfy, config.KindTelegram, config.KindDiscord, config.KindSlack, config.KindGotify}

var pushCheck = config.Check{Name: "api", URL: "https://example.com", Channel: "push"}

func TestNtfyRequest(t *testing.T) {
	p := pushNotifier(t, config.KindNtfy, func(c *config.Channel) { c.ServerURL += "/base" }, nil)
	if err := p.n.Deliver(context.Background(), pushCheck, downResult); err != nil {
		t.Fatal(err)
	}
	r := <-p.got
	if r.method != "POST" || r.path != "/base/up-alerts" {
		t.Fatalf("request %s %s", r.method, r.path)
	}
	for name, want := range map[string]string{"Title": "[up] api is DOWN", "Priority": "high", "Tags": "rotating_light", "Authorization": "Bearer " + ntfyToken} {
		if got := r.header.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if body := string(r.body); !strings.Contains(body, "api is DOWN since Jan 02 18:04:05 (UTC)") || !strings.Contains(body, "Error:  boom") {
		t.Fatalf("body %q", body)
	}

	// Without a token no Authorization is sent; an empty priority is default.
	p = pushNotifier(t, config.KindNtfy, func(c *config.Channel) { c.Token, c.Priority = secret.Sealed{}, "" }, nil)
	if err := p.n.Deliver(context.Background(), pushCheck, check.Result{Status: check.StatusUp, At: deliverAt}); err != nil {
		t.Fatal(err)
	}
	r = <-p.got
	if r.header.Get("Authorization") != "" || r.header.Get("Priority") != "default" || r.header.Get("Tags") != "white_check_mark" {
		t.Fatalf("headers %v", r.header)
	}
}

func TestTelegramRequest(t *testing.T) {
	p := pushNotifier(t, config.KindTelegram, nil, nil)
	if err := p.n.Deliver(context.Background(), pushCheck, downResult); err != nil {
		t.Fatal(err)
	}
	r := <-p.got
	if r.method != "POST" || r.path != "/bot"+telegramToken+"/sendMessage" || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("request %s %s %v", r.method, r.path, r.header)
	}
	b := r.json(t)
	if b["chat_id"] != "-1001234567890" || !strings.HasPrefix(b["text"].(string), "[up] api is DOWN\n\napi is DOWN since") {
		t.Fatalf("body %v", b)
	}
}

func TestDiscordRequest(t *testing.T) {
	p := pushNotifier(t, config.KindDiscord, nil, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	r := check.Result{Status: check.StatusDown, At: deliverAt, Error: "@everyone <@123> broke"}
	if err := p.n.Deliver(context.Background(), pushCheck, r); err != nil {
		t.Fatal(err)
	}
	req := <-p.got
	if req.method != "POST" || req.path != discordPath || req.header.Get("Content-Type") != "application/json" {
		t.Fatalf("request %s %s", req.method, req.path)
	}
	b := req.json(t)
	if content := b["content"].(string); !strings.HasPrefix(content, "[up] api is DOWN\n") || !strings.Contains(content, "@everyone <@123> broke") {
		t.Fatalf("content %q", content)
	}
	// Text from a monitor never pings anyone.
	if mentions, _ := json.Marshal(b["allowed_mentions"]); string(mentions) != `{"parse":[]}` {
		t.Fatalf("allowed_mentions = %s", mentions)
	}
}

func TestSlackRequest(t *testing.T) {
	p := pushNotifier(t, config.KindSlack, nil, nil)
	r := check.Result{Status: check.StatusDown, At: deliverAt, Error: "<!channel> a & b"}
	if err := p.n.Deliver(context.Background(), pushCheck, r); err != nil {
		t.Fatal(err)
	}
	req := <-p.got
	if req.method != "POST" || req.path != slackPath || req.header.Get("Content-Type") != "application/json" {
		t.Fatalf("request %s %s", req.method, req.path)
	}
	// Slack reads <, > and & as markup; they arrive escaped.
	if text := req.json(t)["text"].(string); !strings.HasPrefix(text, "[up] api is DOWN\n") || !strings.Contains(text, "&lt;!channel&gt; a &amp; b") {
		t.Fatalf("text %q", text)
	}
}

func TestGotifyRequest(t *testing.T) {
	p := pushNotifier(t, config.KindGotify, func(c *config.Channel) { c.ServerURL += "/gotify" }, nil)
	if err := p.n.Deliver(context.Background(), pushCheck, downResult); err != nil {
		t.Fatal(err)
	}
	r := <-p.got
	if r.method != "POST" || r.path != "/gotify/message" || r.header.Get("X-Gotify-Key") != gotifyToken || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("request %s %s %v", r.method, r.path, r.header)
	}
	b := r.json(t)
	if b["title"] != "[up] api is DOWN" || !strings.HasPrefix(b["message"].(string), "api is DOWN since") || b["priority"] != float64(8) {
		t.Fatalf("body %v", b)
	}
	p = pushNotifier(t, config.KindGotify, func(c *config.Channel) { c.Priority = "" }, nil)
	if err := p.n.Deliver(context.Background(), pushCheck, downResult); err != nil {
		t.Fatal(err)
	}
	if b := (<-p.got).json(t); b["priority"] != float64(5) {
		t.Fatalf("default priority = %v", b["priority"])
	}
}

// Down, recovery, certificate and test notifications read like the email
// ones on every kind, with the channel's one priority.
func TestPushMessagesForEveryEvent(t *testing.T) {
	for _, kind := range pushKinds {
		t.Run(kind, func(t *testing.T) {
			p := pushNotifier(t, kind, nil, nil)
			ctx := context.Background()
			cert := config.Cert{Check: "api", Issuer: "R3", Observed: deliverAt, NotAfter: deliverAt.Add(120 * time.Hour)}
			for _, tt := range []struct {
				send func() error
				want []string
				tag  string
			}{
				{func() error { return p.n.Deliver(ctx, pushCheck, downResult) }, []string{"[up] api is DOWN", "Error:  boom", "Host:   host"}, "rotating_light"},
				{func() error {
					return p.n.Deliver(ctx, pushCheck, check.Result{Status: check.StatusUp, At: deliverAt, Latency: 20 * time.Millisecond})
				}, []string{"[up] api is UP again", "Latency: 20ms"}, "white_check_mark"},
				{func() error { return p.n.DeliverCert(ctx, pushCheck, cert, 5*24*time.Hour) }, []string{"[up] api: TLS certificate expires in 5 days", "Issuer    R3"}, "warning"},
				{func() error { return p.n.SendTest(ctx, "push") }, []string{"[up] TEST notification from channel push", "no monitor was checked"}, "test_tube"},
			} {
				if err := tt.send(); err != nil {
					t.Fatal(err)
				}
				r := <-p.got
				text := r.text(t, kind)
				for _, want := range tt.want {
					if !strings.Contains(text, want) {
						t.Errorf("text %q lacks %q", text, want)
					}
				}
				switch kind {
				case config.KindNtfy:
					if r.header.Get("Priority") != "high" || r.header.Get("Tags") != tt.tag {
						t.Errorf("ntfy headers %v, want priority high and tag %s", r.header, tt.tag)
					}
				case config.KindGotify:
					if r.json(t)["priority"] != float64(8) {
						t.Errorf("gotify priority %v", r.json(t)["priority"])
					}
				}
			}
		})
	}
}

// Any 2xx is delivered; anything else fails once, without retries or
// redirects, and the error names the status but no secret.
func TestPushStatusHandling(t *testing.T) {
	for _, kind := range pushKinds {
		t.Run(kind, func(t *testing.T) {
			for _, status := range []int{200, 201, 202, 204} {
				p := pushNotifier(t, kind, nil, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
				if err := p.n.Deliver(context.Background(), pushCheck, downResult); err != nil {
					t.Fatalf("status %d: %v", status, err)
				}
			}
			for _, status := range []int{302, 401, 500} {
				var hits int32
				p := pushNotifier(t, kind, nil, func(w http.ResponseWriter, r *http.Request) {
					atomic.AddInt32(&hits, 1)
					w.Header().Set("Location", "/elsewhere")
					w.WriteHeader(status)
					_, _ = io.WriteString(w, r.URL.Path+" "+r.Header.Get("Authorization")+r.Header.Get("X-Gotify-Key"))
				})
				var logs strings.Builder
				p.n.opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))
				err := p.n.Deliver(context.Background(), pushCheck, downResult)
				if err == nil || !strings.Contains(err.Error(), http.StatusText(status)) {
					t.Fatalf("status %d: error = %v", status, err)
				}
				if got := atomic.LoadInt32(&hits); got != 1 {
					t.Fatalf("status %d: %d requests, want 1", status, got)
				}
				for _, leak := range pushSecrets {
					if strings.Contains(err.Error(), leak) || strings.Contains(logs.String(), leak) {
						t.Fatalf("status %d leaks %q: err %v logs %s", status, leak, err, logs.String())
					}
				}
			}
		})
	}
}

// The recorded reason names the kind and, where it has one, the server's
// origin, and never a token or a secret URL, whatever the failure.
func TestPushFailureReasonsCarryNoSecret(t *testing.T) {
	for _, kind := range pushKinds {
		t.Run(kind, func(t *testing.T) {
			prefix := kind + " "
			p := pushNotifier(t, kind, nil, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(401)
				_, _ = io.WriteString(w, r.URL.String()+" "+r.Header.Get("Authorization")+r.Header.Get("X-Gotify-Key"))
			})
			if kind == config.KindTelegram {
				prefix = "telegram: "
			} else {
				prefix += p.srv.URL + ": "
			}
			db := attemptDB(t)
			p.n.opts.Attempts = db
			if err := p.n.Deliver(context.Background(), pushCheck, downResult); err == nil {
				t.Fatal("Deliver succeeded")
			}
			if reason := storedReason(t, db); reason != prefix+"answered 401 Unauthorized" {
				t.Fatalf("reason = %q, want %q", reason, prefix+"answered 401 Unauthorized")
			}

			// A closed server: the transport error carries the URL, the
			// reason does not.
			p = pushNotifier(t, kind, nil, nil)
			p.srv.Close()
			db = attemptDB(t)
			p.n.opts.Attempts = db
			err := p.n.Deliver(context.Background(), pushCheck, downResult)
			if err == nil {
				t.Fatal("Deliver succeeded against a closed server")
			}
			reason := storedReason(t, db)
			for _, leak := range append(pushSecrets, "/bot", "sendMessage", "127.0.0.1:"+strings.Split(p.srv.URL, ":")[2]+"/") {
				if strings.Contains(err.Error(), leak) || strings.Contains(reason, leak) {
					t.Fatalf("leaks %q: err %v reason %q", leak, err, reason)
				}
			}
			if !strings.HasSuffix(reason, "connection failed") {
				t.Fatalf("reason = %q", reason)
			}
		})
	}
}

// Telegram explains a refusal in its answer; the explanation is kept, minus
// the token.
func TestTelegramRefusalKeepsTheDescription(t *testing.T) {
	p := pushNotifier(t, config.KindTelegram, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	})
	db := attemptDB(t)
	p.n.opts.Attempts = db
	if err := p.n.Deliver(context.Background(), pushCheck, downResult); err == nil {
		t.Fatal("Deliver succeeded")
	}
	if reason := storedReason(t, db); reason != "telegram: answered 400 Bad Request (chat not found)" {
		t.Fatalf("reason = %q", reason)
	}
	p = pushNotifier(t, config.KindTelegram, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"ok":false,"description":"Unauthorized: bot `+telegramToken+` blocked"}`)
	})
	err := p.n.Deliver(context.Background(), pushCheck, downResult)
	if err == nil || strings.Contains(err.Error(), "TGSECRET") || !strings.Contains(err.Error(), "answered 401 Unauthorized") {
		t.Fatalf("error = %v", err)
	}
}

func TestPushSecretsNeedTheirKey(t *testing.T) {
	for _, kind := range pushKinds {
		t.Run(kind, func(t *testing.T) {
			p := pushNotifier(t, kind, nil, nil)
			p.n.opts.Key = nil
			if err := p.n.Deliver(context.Background(), pushCheck, downResult); err == nil || !strings.Contains(err.Error(), "no encryption key") {
				t.Fatalf("no key: %v", err)
			}
			other, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
			if err != nil {
				t.Fatal(err)
			}
			p.n.opts.Key = other
			if err := p.n.Deliver(context.Background(), pushCheck, downResult); err == nil || !strings.Contains(err.Error(), "another key") {
				t.Fatalf("wrong key: %v", err)
			}
			if len(p.got) != 0 {
				t.Fatal("a request was sent without the secret")
			}
		})
	}
	// An ntfy channel without a token needs no key.
	p := pushNotifier(t, config.KindNtfy, func(c *config.Channel) { c.Token = secret.Sealed{} }, nil)
	p.n.opts.Key = nil
	if err := p.n.Deliver(context.Background(), pushCheck, downResult); err != nil {
		t.Fatal(err)
	}
}

// A telegram token that is not a bot token is never put in a URL.
func TestTelegramRejectsAMalformedToken(t *testing.T) {
	p := pushNotifier(t, config.KindTelegram, nil, nil)
	key := p.n.opts.Key
	bad, err := secret.Seal(key.PublicKey(), "123:abc/../../TGSECRET?x=")
	if err != nil {
		t.Fatal(err)
	}
	p.ch.Token = bad
	p.n.opts.Store = customStore{found: true, channel: p.ch}
	err = p.n.Deliver(context.Background(), pushCheck, downResult)
	if err == nil || !strings.Contains(err.Error(), "bot token is not valid") || strings.Contains(err.Error(), "TGSECRET") {
		t.Fatalf("error = %v", err)
	}
	if len(p.got) != 0 {
		t.Fatal("a request was sent")
	}
}

// Delivery attempts are recorded for every kind, like email and webhook.
func TestPushAttemptsAreRecorded(t *testing.T) {
	for _, kind := range pushKinds {
		p := pushNotifier(t, kind, nil, nil)
		log := recording(p.n)
		if err := p.n.Deliver(context.Background(), pushCheck, downResult); err != nil {
			t.Fatal(err)
		}
		if err := p.n.SendTest(context.Background(), "push"); err != nil {
			t.Fatal(err)
		}
		got := log.all()
		if len(got) != 1 || got[0].Outcome != store.OutcomeSent || got[0].Event != store.EventDown || got[0].Channel != "push" {
			t.Fatalf("%s attempts = %+v", kind, got)
		}
	}
}
