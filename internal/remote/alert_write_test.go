package remote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

type alertWriteFake struct {
	calls   int
	err     error
	key     []byte
	hasKey  bool
	smtp    config.SMTPServer
	channel config.Channel
	name    string
}

func (f *alertWriteFake) AddSMTPServer(_ context.Context, v config.SMTPServer) error {
	f.calls++
	f.smtp = v
	return f.err
}
func (f *alertWriteFake) UpdateSMTPServer(_ context.Context, v config.SMTPServer) error {
	f.calls++
	f.smtp = v
	return f.err
}
func (f *alertWriteFake) DeleteSMTPServer(_ context.Context, n string) error {
	f.calls++
	f.name = n
	return f.err
}
func (f *alertWriteFake) AddChannel(_ context.Context, c config.Channel) error {
	f.calls++
	f.channel = c
	return f.err
}
func (f *alertWriteFake) UpdateChannel(_ context.Context, c config.Channel) error {
	f.calls++
	f.channel = c
	return f.err
}
func (f *alertWriteFake) DeleteChannel(_ context.Context, n string) error {
	f.calls++
	f.name = n
	return f.err
}
func (f *alertWriteFake) PublicKey(context.Context) ([]byte, bool, error) {
	return f.key, f.hasKey, nil
}

func alertHandler(t *testing.T, s AlertWriteStore) http.Handler {
	t.Helper()
	h, _ := testHandler(t, func(m *http.ServeMux) {
		routes := AlertWriteRoutes(s, nil)
		for _, p := range []string{"/api/v1/smtp-servers", "/api/v1/smtp-servers/", "/api/v1/channels", "/api/v1/channels/"} {
			m.Handle(p, routes)
		}
	})
	return h
}

func jsonHeaders() map[string]string {
	h := bearer(testToken)
	h["Content-Type"] = "application/json"
	return h
}

const (
	smtpBody    = `{"name":"mail","host":"smtp.example.com","port":587,"tls":"starttls","sender":"a@example.com"}`
	channelBody = `{"name":"ops","kind":"email","smtp_server":"mail","recipients":["a@example.com"]}`
)

func TestAlertWriteRoutesStatuses(t *testing.T) {
	f := &alertWriteFake{}
	h := alertHandler(t, f)
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"POST", "/api/v1/smtp-servers", smtpBody, 201},
		{"PUT", "/api/v1/smtp-servers/mail", `{"host":"h.example.com","port":25,"tls":"none","sender":"a@example.com"}`, 204},
		{"PUT", "/api/v1/smtp-servers/mail", `{"name":"mail","host":"h.example.com","port":25,"tls":"none","sender":"a@example.com"}`, 204},
		{"DELETE", "/api/v1/smtp-servers/mail", "", 204},
		{"POST", "/api/v1/channels", channelBody, 201},
		{"PUT", "/api/v1/channels/ops", `{"kind":"email","smtp_server":"mail","recipients":["a@example.com"]}`, 204},
		{"DELETE", "/api/v1/channels/ops", "", 204},
	} {
		if rec := do(h, tc.method, tc.path, strings.NewReader(tc.body), jsonHeaders()); rec.Code != tc.status {
			t.Errorf("%s %s: %d %s, want %d", tc.method, tc.path, rec.Code, rec.Body, tc.status)
		}
	}
}

func TestAlertWriteRoutesRejectBeforeTheStore(t *testing.T) {
	f := &alertWriteFake{}
	h := alertHandler(t, f)
	for _, tc := range []struct {
		label, method, path, body string
		status                    int
		message                   string
	}{
		{"plaintext password", "POST", "/api/v1/smtp-servers", `{"name":"mail","host":"h","port":25,"tls":"none","password":"hunter2"}`, 400, "invalid request body"},
		{"plaintext on update", "PUT", "/api/v1/smtp-servers/mail", `{"password":"hunter2"}`, 400, "invalid request body"},
		{"smtp name mismatch", "PUT", "/api/v1/smtp-servers/mail", `{"name":"other"}`, 400, "name must match path"},
		{"channel name mismatch", "PUT", "/api/v1/channels/ops", `{"name":"other"}`, 400, "name must match path"},
		{"dot smtp", "POST", "/api/v1/smtp-servers", `{"name":"."}`, 400, `name cannot be "." or ".." over the API`},
		{"dotdot channel", "POST", "/api/v1/channels", `{"name":".."}`, 400, `name cannot be "." or ".." over the API`},
		{"unknown field channel", "POST", "/api/v1/channels", `{"name":"ops","admin":true}`, 400, "invalid request body"},
		{"nested path", "DELETE", "/api/v1/smtp-servers/a/b", "", 404, "not found"},
		{"empty name", "DELETE", "/api/v1/channels/", "", 404, "not found"},
		{"sealed data missing", "POST", "/api/v1/smtp-servers", `{"name":"mail","password_sealed":{"key":"AQ=="}}`, 400, "password_sealed.data is required"},
		{"sealed too large", "POST", "/api/v1/smtp-servers", fmt.Sprintf(`{"name":"mail","password_sealed":{"key":"AQ==","data":%q}}`, base64.StdEncoding.EncodeToString(make([]byte, maxSealedBytes+1))), 400, "password_sealed.data is too large"},
		{"no server key", "POST", "/api/v1/smtp-servers", `{"name":"mail","password_sealed":{"key":"AQ==","data":"AQ=="}}`, 400, "the server has no encryption key yet"},
	} {
		rec := do(h, tc.method, tc.path, strings.NewReader(tc.body), jsonHeaders())
		if rec.Code != tc.status || rec.Body.String() != fmt.Sprintf("{\"error\":%q}\n", tc.message) {
			t.Errorf("%s: got %d %q, want %d %q", tc.label, rec.Code, rec.Body, tc.status, tc.message)
		}
	}
	for method, path := range map[string]string{"GET": "/api/v1/smtp-servers", "PUT": "/api/v1/channels", "PATCH": "/api/v1/channels/ops", "POST": "/api/v1/channels/ops"} {
		if rec := do(h, method, path, nil, jsonHeaders()); rec.Code != 405 || rec.Header().Get("Allow") == "" {
			t.Errorf("%s %s: %d allow %q", method, path, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if f.calls != 0 {
		t.Fatalf("store called %d times for rejected requests", f.calls)
	}
}

func TestAlertWriteRoutesNeedAuthentication(t *testing.T) {
	f := &alertWriteFake{}
	h := alertHandler(t, f)
	for _, path := range []string{"/api/v1/smtp-servers", "/api/v1/smtp-servers/mail", "/api/v1/channels", "/api/v1/channels/ops"} {
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			if rec := do(h, method, path, strings.NewReader(smtpBody), map[string]string{"Content-Type": "application/json"}); rec.Code != 401 {
				t.Errorf("%s %s without token: %d", method, path, rec.Code)
			}
		}
	}
	if f.calls != 0 {
		t.Fatalf("unauthenticated requests reached the store %d times", f.calls)
	}
}

func TestSealedPasswordIsCheckedAgainstTheServerKey(t *testing.T) {
	serverKey := bytes.Repeat([]byte{7}, 32)
	otherKey := bytes.Repeat([]byte{9}, 32)
	data := bytes.Repeat([]byte{3}, 48)
	b64 := base64.StdEncoding.EncodeToString
	body := func(key []byte) string {
		return fmt.Sprintf(`{"name":"mail","host":"h","port":25,"tls":"none","username":"u","password_sealed":{"key":%q,"data":%q}}`, b64(key), b64(data))
	}
	f := &alertWriteFake{key: serverKey, hasKey: true}
	h := alertHandler(t, f)

	rec := do(h, "POST", "/api/v1/smtp-servers", strings.NewReader(body(otherKey)), jsonHeaders())
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "password was sealed to a different key") || f.calls != 0 {
		t.Fatalf("other key: %d %s calls=%d", rec.Code, rec.Body, f.calls)
	}
	rec = do(h, "POST", "/api/v1/smtp-servers", strings.NewReader(body(serverKey)), jsonHeaders())
	if rec.Code != 201 || f.calls != 1 {
		t.Fatalf("server key: %d %s", rec.Code, rec.Body)
	}
	if !bytes.Equal(f.smtp.Password.Data, data) || !bytes.Equal(f.smtp.Password.Key, serverKey) {
		t.Errorf("sealed value not stored as sent: %+v", f.smtp.Password)
	}
	if !strings.Contains(rec.Body.String(), `"password_state":"set"`) {
		t.Errorf("response lacks the state: %s", rec.Body)
	}
	for _, secretBytes := range [][]byte{data, []byte(b64(data))} {
		if bytes.Contains(rec.Body.Bytes(), secretBytes) {
			t.Errorf("response leaks sealed bytes: %s", rec.Body)
		}
	}
	// Without password_sealed the store gets an empty Sealed (keep or none).
	f.smtp = config.SMTPServer{Password: secret.Sealed{Data: []byte{1}}}
	rec = do(h, "PUT", "/api/v1/smtp-servers/mail", strings.NewReader(`{"host":"h","port":25,"tls":"none"}`), jsonHeaders())
	if rec.Code != 204 || !f.smtp.Password.Empty() {
		t.Errorf("omitted password must reach the store empty: %d %+v", rec.Code, f.smtp.Password)
	}
}

func TestAlertWriteRoutesNames(t *testing.T) {
	f := &alertWriteFake{}
	h := alertHandler(t, f)
	for _, name := range []string{"my mail", "café ☕", "a/b", "100%"} {
		f.name = ""
		if rec := do(h, "DELETE", "/api/v1/smtp-servers/"+url.PathEscape(name), nil, bearer(testToken)); rec.Code != 204 || f.name != name {
			t.Errorf("smtp %q: %d, store got %q", name, rec.Code, f.name)
		}
		f.name = ""
		if rec := do(h, "DELETE", "/api/v1/channels/"+url.PathEscape(name), nil, bearer(testToken)); rec.Code != 204 || f.name != name {
			t.Errorf("channel %q: %d, store got %q", name, rec.Code, f.name)
		}
	}
}

func TestAlertWriteRoutesMapRealStoreErrors(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := alertHandler(t, s)
	ctx := context.Background()
	if err := s.AddCheck(ctx, config.Check{Name: "web", URL: "https://example.com", Interval: 60e9, Timeout: 1e9, ExpectedStatus: 200}); err != nil {
		t.Fatal(err)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		return do(h, "POST", path, strings.NewReader(body), jsonHeaders())
	}
	if rec := post("/api/v1/smtp-servers", smtpBody); rec.Code != 201 {
		t.Fatalf("create smtp: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/v1/channels", channelBody); rec.Code != 201 {
		t.Fatalf("create channel: %d %s", rec.Code, rec.Body)
	}
	if err := s.UpdateCheck(ctx, config.Check{Name: "web", URL: "https://example.com", Interval: 60e9, Timeout: 1e9, ExpectedStatus: 200, Channel: "ops"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label, method, path, body string
		status                    int
		message                   string
	}{
		{"duplicate smtp", "POST", "/api/v1/smtp-servers", smtpBody, 409, `store: add SMTP server: name "mail" already exists`},
		{"duplicate channel", "POST", "/api/v1/channels", channelBody, 409, `store: add channel: name "ops" already exists`},
		{"update unknown smtp", "PUT", "/api/v1/smtp-servers/ghost", `{"host":"h.example.com","port":25,"tls":"none","sender":"a@example.com"}`, 404, `store: update SMTP server: name "ghost" not found`},
		{"update unknown channel", "PUT", "/api/v1/channels/ghost", `{"kind":"email","smtp_server":"mail","recipients":["a@example.com"]}`, 404, `store: update channel: name "ghost" not found`},
		{"delete unknown smtp", "DELETE", "/api/v1/smtp-servers/ghost", "", 404, `store: delete SMTP server: name "ghost" not found`},
		{"delete unknown channel", "DELETE", "/api/v1/channels/ghost", "", 404, `store: delete channel: name "ghost" not found`},
		{"invalid host", "POST", "/api/v1/smtp-servers", `{"name":"x","host":"http://bad","port":25,"tls":"none"}`, 400, "host"},
		{"tls none with username", "POST", "/api/v1/smtp-servers", `{"name":"x","host":"h.example.com","port":25,"tls":"none","username":"u"}`, 400, `TLS "none" cannot be used with a username`},
		{"channel without recipients", "POST", "/api/v1/channels", `{"name":"x","kind":"email","smtp_server":"mail","recipients":[]}`, 400, "at least one recipient is required"},
		{"channel with unknown server", "POST", "/api/v1/channels", `{"name":"x","kind":"email","smtp_server":"ghost","recipients":["a@example.com"]}`, 400, `SMTP server "ghost" does not exist`},
		{"bad email", "POST", "/api/v1/channels", `{"name":"x","kind":"email","smtp_server":"mail","recipients":["nope"]}`, 400, `recipient "nope" is not a valid email address`},
		{"smtp in use", "DELETE", "/api/v1/smtp-servers/mail", "", 409, "ops"},
		{"channel in use", "DELETE", "/api/v1/channels/ops", "", 409, "web"},
	} {
		rec := do(h, tc.method, tc.path, strings.NewReader(tc.body), jsonHeaders())
		var e struct{ Error string }
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != tc.status || !strings.Contains(e.Error, tc.message) {
			t.Errorf("%s: got %d %q, want %d containing %q", tc.label, rec.Code, e.Error, tc.status, tc.message)
		}
	}
	// Unclassified errors never leak their text.
	failing := &alertWriteFake{err: errors.New("database is locked: secret detail")}
	rec := do(alertHandler(t, failing), "DELETE", "/api/v1/channels/ops", nil, bearer(testToken))
	if rec.Code != 500 || rec.Body.String() != "{\"error\":\"internal error\"}\n" {
		t.Errorf("500 body: %d %q", rec.Code, rec.Body)
	}
}
