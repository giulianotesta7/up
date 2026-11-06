package client

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/connection"
	"github.com/giulianotesta7/up/internal/remote"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

const testToken = "up_" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE"

// noToken fails when err mentions the token: no error may carry the secret.
func noToken(t *testing.T, label string, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), testToken) {
		t.Errorf("%s: error leaks the token: %v", label, err)
	}
}

// realServer serves the real API handlers over a real store, so the client is
// tested against the actual contract, not a copy of it.
func realServer(t *testing.T) (*Client, *store.Store, *secret.Key) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	key, _, err := secret.LoadOrCreate(filepath.Join(dir, "up.db.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicKey(context.Background(), key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := remote.SaveToken(filepath.Join(dir, "token"), testToken); err != nil {
		t.Fatal(err)
	}
	verifier, err := remote.LoadVerifier(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/snapshot", remote.SnapshotRoutes(func(ctx context.Context, now time.Time) (tui.Snapshot, error) { return tui.Load(ctx, s, now) }))
	writes, alerts := remote.WriteRoutes(s), remote.AlertWriteRoutes(s)
	for _, p := range []string{"/api/v1/checks", "/api/v1/checks/"} {
		mux.Handle(p, writes)
	}
	for _, p := range []string{"/api/v1/smtp-servers", "/api/v1/smtp-servers/", "/api/v1/channels", "/api/v1/channels/"} {
		mux.Handle(p, alerts)
	}
	maintenance := remote.MaintenanceRoutes(s)
	mux.Handle("/api/v1/checks/{name}/maintenance", maintenance)
	mux.Handle("/api/v1/checks/{name}/maintenance/{id}", maintenance)
	mux.Handle("/api/v1/channels/{name}/test", remote.ChannelTestRoutes(s))
	mux.Handle("/api/v1/defaults", remote.DefaultsRoutes(s))
	mux.Handle("/api/v1/deliveries", remote.DeliveryRoutes(s))
	srv := httptest.NewServer(remote.NewHandler(remote.Options{Verifier: verifier, Version: "test", Routes: mux}))
	t.Cleanup(srv.Close)
	return New(srv.URL, testToken), s, key
}

func newCheck(name string) config.Check {
	return config.Check{Name: name, URL: "https://example.com", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200}
}

func TestEndToEndAgainstTheRealAPI(t *testing.T) {
	c, _, key := realServer(t)
	ctx := context.Background()
	load := func() tui.Snapshot {
		snap, err := c.LoadSnapshot(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return snap
	}
	name := "my site/é%"
	if err := c.AddCheck(ctx, newCheck(name)); err != nil {
		t.Fatalf("AddCheck: %v", err)
	}
	updated := newCheck(name)
	updated.URL = "https://example.com/health"
	updated.ExpectedStatus = 204
	if err := c.UpdateCheck(ctx, updated); err != nil {
		t.Fatalf("UpdateCheck: %v", err)
	}
	snap := load()
	if len(snap.Services) != 1 || snap.Services[0].Check.URL != updated.URL || snap.Services[0].Check.ExpectedStatus != 204 {
		t.Fatalf("snapshot after update: %+v", snap.Services)
	}

	// The TUI's shortest and longest mute choices must be accepted even though
	// a little time passes between computing `until` and sending the request.
	for _, d := range []time.Duration{tui.MinMute, tui.MaxMute, 90 * time.Minute} {
		if err := c.SetMute(ctx, name, time.Now().Add(d), false); err != nil {
			t.Errorf("SetMute(%v): %v", d, err)
		}
	}
	if s := load().Services[0]; !s.HasMute || !s.Muted(time.Now()) {
		t.Errorf("mute not applied: %+v", s.Mute)
	}
	if err := c.Unmute(ctx, name, time.Now()); err != nil {
		t.Fatalf("Unmute: %v", err)
	}
	if s := load().Services[0]; s.Muted(time.Now()) {
		t.Error("still muted after Unmute")
	}

	sealed, err := secret.Seal(key.PublicKey(), "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	smtp := config.SMTPServer{Name: "gmail", Host: "smtp.example.com", Port: 465, TLS: config.TLSImplicit, Username: "u@example.com", Sender: "u@example.com", Password: sealed}
	if err := c.AddSMTPServer(ctx, smtp); err != nil {
		t.Fatalf("AddSMTPServer: %v", err)
	}
	snap = load()
	if len(snap.SMTPServers) != 1 || snap.SMTPServers[0].Password.Empty() || string(snap.SMTPServers[0].Password.Key) != string(snap.PublicKey) {
		t.Fatalf("a sealed password must show as set: %+v", snap.SMTPServers)
	}
	smtp.Password = secret.Sealed{} // edit without a new password keeps the old one
	smtp.Host = "smtp2.example.com"
	if err := c.UpdateSMTPServer(ctx, smtp); err != nil {
		t.Fatalf("UpdateSMTPServer: %v", err)
	}
	if got := load().SMTPServers[0]; got.Host != "smtp2.example.com" || got.Password.Empty() {
		t.Fatalf("password must be kept: %+v", got)
	}
	channel := config.Channel{Name: "ops team", Kind: "email", SMTPServer: "gmail", Recipients: []string{"ops@example.com"}}
	if err := c.AddChannel(ctx, channel); err != nil {
		t.Fatalf("AddChannel: %v", err)
	}
	channel.Recipients = []string{"ops@example.com", "dev@example.com"}
	if err := c.UpdateChannel(ctx, channel); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	if got := load().Channels[0]; len(got.Recipients) != 2 {
		t.Fatalf("channel not updated: %+v", got)
	}

	// Server-side messages arrive exactly as the local TUI would show them.
	for _, tc := range []struct {
		label string
		err   error
		want  string
	}{
		{"duplicate", c.AddCheck(ctx, newCheck(name)), fmt.Sprintf("store: add check: name %q already exists", name)},
		{"unknown update", c.UpdateCheck(ctx, newCheck("ghost")), `store: update check: name "ghost" not found`},
		{"unknown delete", c.DeleteCheck(ctx, "ghost"), `store: delete check: name "ghost" not found`},
		{"smtp in use", c.DeleteSMTPServer(ctx, "gmail"), "ops team"},
		{"bad channel server", c.AddChannel(ctx, config.Channel{Name: "x", Kind: "email", SMTPServer: "ghost", Recipients: []string{"a@example.com"}}), `SMTP server "ghost" does not exist`},
	} {
		if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", tc.label, tc.err, tc.want)
		}
		if tc.err != nil && strings.Contains(tc.err.Error(), "internal error") {
			t.Errorf("%s: unexpected 500: %v", tc.label, tc.err)
		}
	}
	for _, step := range []func() error{
		func() error { return c.DeleteChannel(ctx, "ops team") },
		func() error { return c.DeleteSMTPServer(ctx, "gmail") },
		func() error { return c.DeleteCheck(ctx, name) },
	} {
		if err := step(); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	if snap := load(); len(snap.Services)+len(snap.SMTPServers)+len(snap.Channels) != 0 {
		t.Fatalf("leftovers: %+v", snap)
	}
}

func TestNamesThatCannotBeAddressedAreRefusedLocally(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	c := New(srv.URL, testToken)
	for _, name := range []string{".", ".."} {
		for label, err := range map[string]error{
			"update check": c.UpdateCheck(context.Background(), newCheck(name)),
			"delete check": c.DeleteCheck(context.Background(), name),
			"mute":         c.SetMute(context.Background(), name, time.Now().Add(time.Hour), false),
			"unmute":       c.Unmute(context.Background(), name, time.Now()),
			"delete smtp":  c.DeleteSMTPServer(context.Background(), name),
			"delete chan":  c.DeleteChannel(context.Background(), name),
			"add check":    c.AddCheck(context.Background(), newCheck(name)),
			"add smtp":     c.AddSMTPServer(context.Background(), config.SMTPServer{Name: name, Host: "h", Port: 25, TLS: config.TLSNone}),
			"add channel":  c.AddChannel(context.Background(), config.Channel{Name: name, Kind: "email", SMTPServer: "m", Recipients: []string{"a@example.com"}}),
		} {
			if err == nil || !strings.Contains(err.Error(), `name cannot be "." or ".." over the API`) {
				t.Errorf("%s %q: %v", label, name, err)
			}
		}
	}
	if hits != 0 {
		t.Fatalf("%d requests were sent for names the server cannot address", hits)
	}
}

func TestClockSkewIsRemovedFromEveryTime(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{2 * time.Hour, -2 * time.Hour, 0} {
		serverNow := now.Add(offset)
		beat := serverNow.Add(-5 * time.Second)
		snap := tui.Snapshot{
			HasBeat: true, LastBeat: beat,
			Services: []tui.Service{{
				Check: newCheck("api"), HasResult: true,
				Latest:    check.Result{CheckName: "api", At: serverNow.Add(-10 * time.Second), Status: check.StatusUp},
				DownSince: serverNow.Add(-time.Hour),
				Recent:    []check.Result{{CheckName: "api", At: serverNow.Add(-20 * time.Second), Status: check.StatusUp}},
				Outages:   []store.Outage{{Start: serverNow.Add(-3 * time.Hour), End: serverNow.Add(-2 * time.Hour)}},
				HasMute:   true, Mute: config.Mute{Check: "api", Until: serverNow.Add(30 * time.Minute)},
				HasCert: true, Cert: config.Cert{Check: "api", Host: "example.com", NotAfter: serverNow.Add(240 * time.Hour), Observed: serverNow.Add(-time.Minute)},
			}, {Check: newCheck("idle")}},
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(remote.NewSnapshotDTO(snap, serverNow))
		}))
		got, err := New(srv.URL, testToken).LoadSnapshot(context.Background(), now)
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		want := func(label string, got time.Time, expected time.Time) {
			if !got.Equal(expected) {
				t.Errorf("offset %v: %s = %v, want %v", offset, label, got, expected)
			}
		}
		want("LastBeat", got.LastBeat, now.Add(-5*time.Second))
		s := got.Services[0]
		want("Latest.At", s.Latest.At, now.Add(-10*time.Second))
		want("DownSince", s.DownSince, now.Add(-time.Hour))
		want("Recent", s.Recent[0].At, now.Add(-20*time.Second))
		want("Outage.Start", s.Outages[0].Start, now.Add(-3*time.Hour))
		want("Outage.End", s.Outages[0].End, now.Add(-2*time.Hour))
		want("Mute.Until", s.Mute.Until, now.Add(30*time.Minute))
		want("Cert.NotAfter", s.Cert.NotAfter, now.Add(240*time.Hour))
		want("Cert.Observed", s.Cert.Observed, now.Add(-time.Minute))
		idle := got.Services[1]
		if !idle.DownSince.IsZero() || !idle.Latest.At.IsZero() || !idle.Mute.Until.IsZero() {
			t.Errorf("offset %v: zero times must stay zero: %+v", offset, idle)
		}
		if got.Stale(now) {
			t.Errorf("offset %v: a fresh heartbeat must not look stale", offset)
		}
	}
}

func TestReadMethodsFailLoudly(t *testing.T) {
	c := New("https://example.invalid", testToken)
	ctx := context.Background()
	_, e1 := c.Checks(ctx)
	_, e2 := c.Recent(ctx, "a", 1)
	_, e3 := c.Outages(ctx, "a", 1)
	_, _, e4 := c.LastBeat(ctx)
	_, _, _, e5 := c.UptimeWindow(ctx, "a", time.Now(), time.Now())
	_, e6 := c.Mutes(ctx)
	_, e7 := c.Certs(ctx)
	_, e8 := c.SMTPServers(ctx)
	_, e9 := c.Channels(ctx)
	_, _, e10 := c.PublicKey(ctx)
	for i, err := range []error{e1, e2, e3, e4, e5, e6, e7, e8, e9, e10} {
		if err == nil || err.Error() != "client: reads go through LoadSnapshot" {
			t.Errorf("read %d: %v", i+1, err)
		}
	}
}

type captured struct {
	method, rawPath, query, auth, agent, contentType, accept, body string
}

func capture(t *testing.T, status int) (*Client, func() captured) {
	t.Helper()
	var mu sync.Mutex
	var last captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = captured{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("Content-Type"), r.Header.Get("Accept"), string(body)}
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, testToken), func() captured { mu.Lock(); defer mu.Unlock(); return last }
}

func TestWriteRequestsAreExact(t *testing.T) {
	c, last := capture(t, 204)
	ctx := context.Background()
	name := "my site/é%?#"
	escaped := url.PathEscape(name)
	sealed := secret.Sealed{Key: []byte{1, 2, 3}, Data: []byte{4, 5, 6}}
	until := time.Now().Add(2 * time.Hour)
	for _, tc := range []struct {
		label, method, path string
		run                 func() error
		body                string
	}{
		{"add check", "POST", "/api/v1/checks", func() error { return c.AddCheck(ctx, newCheck("a")) }, `{"name":"a","url":"https://example.com","interval_ms":3600000,"timeout_ms":1000,"expected_status":200,"channel":"","alert_after":0,"recover_after":0,"tls_off":false}`},
		{"update check", "PUT", "/api/v1/checks/" + escaped, func() error { return c.UpdateCheck(ctx, newCheck(name)) }, ""},
		{"delete check", "DELETE", "/api/v1/checks/" + escaped, func() error { return c.DeleteCheck(ctx, name) }, ""},
		{"mute", "PUT", "/api/v1/checks/" + escaped + "/mute", func() error { return c.SetMute(ctx, name, until, true) }, ""},
		{"unmute", "DELETE", "/api/v1/checks/" + escaped + "/mute", func() error { return c.Unmute(ctx, name, time.Now()) }, ""},
		{"add smtp", "POST", "/api/v1/smtp-servers", func() error {
			return c.AddSMTPServer(ctx, config.SMTPServer{Name: "m", Host: "h", Port: 25, TLS: config.TLSNone, Sender: "a@example.com"})
		}, `{"name":"m","host":"h","port":25,"tls":"none","username":"","sender":"a@example.com"}`},
		{"update smtp", "PUT", "/api/v1/smtp-servers/" + escaped, func() error {
			return c.UpdateSMTPServer(ctx, config.SMTPServer{Name: name, Host: "h", Port: 25, TLS: config.TLSNone})
		}, ""},
		{"delete smtp", "DELETE", "/api/v1/smtp-servers/" + escaped, func() error { return c.DeleteSMTPServer(ctx, name) }, ""},
		{"add channel", "POST", "/api/v1/channels", func() error {
			return c.AddChannel(ctx, config.Channel{Name: "c", Kind: "email", SMTPServer: "m", Recipients: []string{"a@example.com"}})
		}, `{"name":"c","kind":"email","smtp_server":"m","recipients":["a@example.com"]}`},
		{"update channel", "PUT", "/api/v1/channels/" + escaped, func() error { return c.UpdateChannel(ctx, config.Channel{Name: name}) }, ""},
		{"delete channel", "DELETE", "/api/v1/channels/" + escaped, func() error { return c.DeleteChannel(ctx, name) }, ""},
	} {
		if err := tc.run(); err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		got := last()
		if got.method != tc.method || got.rawPath != tc.path {
			t.Errorf("%s: %s %s, want %s %s", tc.label, got.method, got.rawPath, tc.method, tc.path)
		}
		if got.auth != "Bearer "+testToken || got.agent != "up" || got.accept != "application/json" || got.query != "" {
			t.Errorf("%s: headers %+v", tc.label, got)
		}
		if tc.body != "" && strings.TrimSpace(got.body) != tc.body {
			t.Errorf("%s: body %s, want %s", tc.label, got.body, tc.body)
		}
		hasBody := tc.method != "DELETE"
		if hasBody != (got.contentType == "application/json") {
			t.Errorf("%s: content type %q", tc.label, got.contentType)
		}
		if strings.Contains(got.rawPath, testToken) || strings.Contains(got.query, testToken) || strings.Contains(got.body, testToken) {
			t.Errorf("%s: the token appears outside the Authorization header: %+v", tc.label, got)
		}
	}
	// Mute duration is computed from `until`, to the nearest second.
	_ = c.SetMute(ctx, "a", time.Now().Add(time.Minute), true)
	var mute struct {
		DurationMS  int64 `json:"duration_ms"`
		WasAlerting bool  `json:"was_alerting"`
	}
	if err := json.Unmarshal([]byte(last().body), &mute); err != nil || mute.DurationMS != 60000 || !mute.WasAlerting {
		t.Errorf("mute body %s: %+v %v", last().body, mute, err)
	}
	_ = c.SetMute(ctx, "a", time.Now().Add(90*time.Minute+400*time.Millisecond), false)
	if err := json.Unmarshal([]byte(last().body), &mute); err != nil || mute.DurationMS != 5400000 {
		t.Errorf("mute rounding: %s", last().body)
	}
	// Passwords: sealed goes as base64 key/data, empty is omitted, never plaintext.
	_ = c.AddSMTPServer(ctx, config.SMTPServer{Name: "m", Host: "h", Port: 465, TLS: config.TLSImplicit, Username: "u", Password: sealed})
	var got struct {
		Sealed *struct{ Key, Data []byte } `json:"password_sealed"`
	}
	if err := json.Unmarshal([]byte(last().body), &got); err != nil || got.Sealed == nil || string(got.Sealed.Key) != "\x01\x02\x03" || string(got.Sealed.Data) != "\x04\x05\x06" {
		t.Errorf("sealed password body: %s", last().body)
	}
	if !strings.Contains(last().body, `"key":"AQID"`) || !strings.Contains(last().body, `"data":"BAUG"`) {
		t.Errorf("sealed must be base64: %s", last().body)
	}
	_ = c.UpdateSMTPServer(ctx, config.SMTPServer{Name: "m", Host: "h", Port: 465, TLS: config.TLSImplicit, Username: "u"})
	if strings.Contains(last().body, "password") {
		t.Errorf("an empty password must not send any password field: %s", last().body)
	}
}

func TestResponsesBecomeErrors(t *testing.T) {
	for _, tc := range []struct {
		label  string
		status int
		body   string
		want   string
	}{
		{"validation text", 400, `{"error":"name is required"}`, "name is required"},
		{"not found text", 404, `{"error":"store: delete check: name \"x\" not found"}`, `store: delete check: name "x" not found`},
		{"conflict text", 409, `{"error":"store: add check: name \"x\" already exists"}`, `store: add check: name "x" already exists`},
		{"internal", 500, `{"error":"internal error"}`, "internal error"},
		{"html gateway", 502, `<html>bad gateway</html>`, "server returned 502"},
		{"empty error", 503, `{"error":""}`, "server returned 503"},
		{"token echoed", 400, `{"error":"bad ` + testToken + `"}`, "server returned an error"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		c := New(srv.URL, testToken)
		for label, err := range map[string]error{"write": c.DeleteCheck(context.Background(), "x"), "snapshot": func() error { _, e := c.LoadSnapshot(context.Background(), time.Now()); return e }()} {
			if err == nil || err.Error() != tc.want {
				t.Errorf("%s/%s: got %v, want %q", tc.label, label, err, tc.want)
			}
			noToken(t, tc.label+"/"+label, err)
		}
		srv.Close()
	}
	unauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer unauthorized.Close()
	c := New(unauthorized.URL, testToken)
	if err := c.DeleteCheck(context.Background(), "x"); !errors.Is(err, connection.ErrTokenRejected) {
		t.Errorf("401 write: %v", err)
	}
	if _, err := c.LoadSnapshot(context.Background(), time.Now()); !errors.Is(err, connection.ErrTokenRejected) {
		t.Errorf("401 snapshot: %v", err)
	}
	notJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "not json") }))
	defer notJSON.Close()
	if _, err := New(notJSON.URL, testToken).LoadSnapshot(context.Background(), time.Now()); err == nil || err.Error() != "client: invalid snapshot" {
		t.Errorf("bad snapshot body: %v", err)
	}
}

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		label   string
		status  int
		body    string
		want    error
		message string
	}{
		{"ok", 200, `{"name":"up","version":"x"}`, nil, ""},
		{"other service", 200, `{"name":"nginx"}`, errIncompatible, ""},
		{"not json", 200, `hello`, errIncompatible, ""},
		{"not found", 404, `{"error":"not found"}`, errIncompatible, ""},
		{"server error", 500, `{"error":"internal error"}`, errIncompatible, ""},
		{"token rejected", 401, `{"error":"unauthorized"}`, connection.ErrTokenRejected, ""},
		{"redirect", 302, ``, errRedirect, ""},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/ping" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+testToken {
				t.Errorf("%s: unexpected request %s %s", tc.label, r.Method, r.URL)
			}
			if tc.status == 302 {
				w.Header().Set("Location", "https://elsewhere.example/")
			}
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		err := Validate(context.Background(), srv.URL, testToken)
		srv.Close()
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.label, err, tc.want)
		}
		noToken(t, tc.label, err)
	}
	var _ tui.LoginValidator = Validate
}

func TestRedirectsAreNeverFollowedAndNeverForwardTheToken(t *testing.T) {
	var forwarded int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++ }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	c := New(origin.URL, testToken)
	if err := c.AddCheck(context.Background(), newCheck("a")); !errors.Is(err, errRedirect) && (err == nil || err.Error() != errRedirect.Error()) {
		t.Errorf("write: %v", err)
	}
	if _, err := c.LoadSnapshot(context.Background(), time.Now()); err == nil || err.Error() != errRedirect.Error() {
		t.Errorf("snapshot: %v", err)
	}
	if forwarded != 0 {
		t.Fatalf("the redirect target received %d requests", forwarded)
	}
}

func TestTransportFailures(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	address := closed.URL
	closed.Close()
	for label, err := range map[string]error{
		"validate": Validate(context.Background(), address, testToken),
		"snapshot": func() error { _, e := New(address, testToken).LoadSnapshot(context.Background(), time.Now()); return e }(),
		"write":    New(address, testToken).DeleteCheck(context.Background(), "a"),
	} {
		if !errors.Is(err, connection.ErrUnreachable) || !strings.Contains(err.Error(), "refused") {
			t.Errorf("%s: %v", label, err)
		}
		noToken(t, label, err)
	}

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"name":"up"}`) }))
	defer tlsServer.Close()
	for label, err := range map[string]error{
		"validate": Validate(context.Background(), tlsServer.URL, testToken),
		"snapshot": func() error {
			_, e := New(tlsServer.URL, testToken).LoadSnapshot(context.Background(), time.Now())
			return e
		}(),
		"write": New(tlsServer.URL, testToken).DeleteCheck(context.Background(), "a"),
	} {
		if !errors.Is(err, connection.ErrCertUntrusted) {
			t.Errorf("untrusted certificate, %s: %v", label, err)
		}
		noToken(t, label, err)
	}
	for _, cause := range []error{x509.HostnameError{Host: "a.example"}, x509.CertificateInvalidError{Reason: x509.Expired}, x509.UnknownAuthorityError{}} {
		if err := transportError(fmt.Errorf("Get: %w", cause)); !errors.Is(err, connection.ErrCertUntrusted) {
			t.Errorf("%T: %v", cause, err)
		}
	}
}

func TestTimeoutsAndCancellationReturnPromptly(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)

	c := New(slow.URL, testToken)
	c.http.Timeout = 80 * time.Millisecond
	start := time.Now()
	_, err := c.LoadSnapshot(context.Background(), time.Now())
	if !errors.Is(err, connection.ErrUnreachable) || !strings.Contains(err.Error(), "timed out") || time.Since(start) > 3*time.Second {
		t.Errorf("timeout: %v after %v", err, time.Since(start))
	}
	noToken(t, "timeout", err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start = time.Now()
	err = Validate(ctx, slow.URL, testToken)
	if !errors.Is(err, connection.ErrUnreachable) || !strings.Contains(err.Error(), "canceled") || time.Since(start) > 3*time.Second {
		t.Errorf("cancel: %v after %v", err, time.Since(start))
	}
}

func TestOversizedResponsesAreRefused(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 1<<20)
		for i := range chunk {
			chunk[i] = ' '
		}
		for i := 0; i < 17; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer big.Close()
	if _, err := New(big.URL, testToken).LoadSnapshot(context.Background(), time.Now()); err == nil || err.Error() != "server response too large" {
		t.Errorf("oversized response: %v", err)
	}
}

func TestInvalidHeaderValuesNeverLeakTheToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	bad := "up_abc\r\nX-Injected: 1"
	err := Validate(context.Background(), srv.URL, bad)
	if err == nil || strings.Contains(err.Error(), "abc") {
		t.Errorf("a malformed token must fail without echoing it: %v", err)
	}
}

func TestPlainHTTPNeverUsesAProxy(t *testing.T) {
	// A proxy would see the bearer token of a plain http request, which is
	// already unencrypted; https goes through the usual environment rules.
	plain, _ := http.NewRequest("GET", "http://up.acme.com/api/v1/ping", nil)
	if got, err := proxyFor(plain); got != nil || err != nil {
		t.Errorf("plain http must bypass any proxy: %v %v", got, err)
	}
	secure, _ := http.NewRequest("GET", "https://up.acme.com/api/v1/ping", nil)
	want, wantErr := http.ProxyFromEnvironment(secure)
	if got, err := proxyFor(secure); (got == nil) != (want == nil) || (err == nil) != (wantErr == nil) || (got != nil && got.String() != want.String()) {
		t.Errorf("https must follow the environment: got %v %v, want %v %v", got, err, want, wantErr)
	}
}
