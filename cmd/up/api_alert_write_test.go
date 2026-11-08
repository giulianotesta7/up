package main

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/heohlee/up/internal/remote"
	"github.com/heohlee/up/internal/secret"
)

func TestAPIAlertWritesKeepPasswordsSealed(t *testing.T) {
	call := startTestAPI(t)
	var bodies []string
	do := func(method, path, body string) (int, string) {
		code, out := call(method, path, body)
		bodies = append(bodies, out)
		return code, out
	}
	snapshot := func() remote.SnapshotDTO {
		code, body := do("GET", "/api/v1/snapshot", "")
		var d remote.SnapshotDTO
		if code != 200 || json.Unmarshal([]byte(body), &d) != nil {
			t.Fatalf("snapshot: %d %s", code, body)
		}
		return d
	}
	state := func(d remote.SnapshotDTO) string {
		out, _ := json.Marshal([]any{d.SMTPServers, d.Channels})
		return string(out)
	}
	passwordState := func(name string) string {
		for _, s := range snapshot().SMTPServers {
			if s.Name == name {
				return s.PasswordState
			}
		}
		return "missing"
	}

	d := snapshot()
	if !d.HasKey || len(d.PublicKey) == 0 {
		t.Fatalf("serve must have created a key: %+v", d)
	}
	sealedTo := func(key []byte, plaintext string) (string, secret.Sealed) {
		sealed, err := secret.Seal(key, plaintext)
		if err != nil {
			t.Fatal(err)
		}
		return `"password_sealed":{"key":"` + base64.StdEncoding.EncodeToString(sealed.Key) + `","data":"` + base64.StdEncoding.EncodeToString(sealed.Data) + `"}`, sealed
	}
	field, sealed := sealedTo(d.PublicKey, "hunter2")
	smtp := func(extra string) string {
		return `{"name":"gmail","host":"smtp.example.com","port":465,"tls":"tls","username":"alerts@example.com","sender":"alerts@example.com"` + extra + `}`
	}
	if code, body := do("POST", "/api/v1/smtp-servers", smtp(","+field)); code != 201 || !strings.Contains(body, `"password_state":"set"`) {
		t.Fatalf("create smtp: %d %s", code, body)
	}
	if got := passwordState("gmail"); got != "set" {
		t.Fatalf("password_state = %q, want set", got)
	}

	// A PUT without password_sealed keeps the stored password.
	if code, body := do("PUT", "/api/v1/smtp-servers/gmail", smtp("")); code != 204 {
		t.Fatalf("update keeps password: %d %s", code, body)
	}
	if got := passwordState("gmail"); got != "set" {
		t.Fatalf("after update without password: %q, want set", got)
	}

	// Sealed to another key: rejected, state unchanged.
	otherKey := append([]byte(nil), d.PublicKey...)
	otherKey[0] ^= 0xff
	foreign := `"password_sealed":{"key":"` + base64.StdEncoding.EncodeToString(otherKey) + `","data":"` + base64.StdEncoding.EncodeToString(sealed.Data) + `"}`
	before := state(snapshot())
	if code, body := do("PUT", "/api/v1/smtp-servers/gmail", smtp(","+foreign)); code != 400 || !strings.Contains(body, "password was sealed to a different key") {
		t.Fatalf("foreign key: %d %s", code, body)
	}
	if state(snapshot()) != before {
		t.Fatal("a rejected password changed the stored servers")
	}

	// Without a username the password is removed, as in the local TUI.
	if code, body := do("PUT", "/api/v1/smtp-servers/gmail", `{"host":"smtp.example.com","port":465,"tls":"tls","sender":"alerts@example.com"}`); code != 204 {
		t.Fatalf("remove username: %d %s", code, body)
	}
	if got := passwordState("gmail"); got != "none" {
		t.Fatalf("after removing the username: %q, want none", got)
	}
	if code, _ := do("PUT", "/api/v1/smtp-servers/gmail", smtp(","+field)); code != 204 {
		t.Fatal("restoring the password failed")
	}
	if got := passwordState("gmail"); got != "set" {
		t.Fatalf("restored password: %q, want set", got)
	}

	// Channels and in-use deletes.
	channel := `{"name":"ops team","kind":"email","smtp_server":"gmail","recipients":["ops@example.com"]}`
	if code, body := do("POST", "/api/v1/channels", channel); code != 201 {
		t.Fatalf("create channel: %d %s", code, body)
	}
	monitor := `{"name":"web","url":"https://example.com","interval_ms":3600000,"timeout_ms":1000,"expected_status":200,"channel":"ops team"}`
	if code, body := do("POST", "/api/v1/checks", monitor); code != 201 {
		t.Fatalf("create monitor: %d %s", code, body)
	}
	if code, body := do("DELETE", "/api/v1/smtp-servers/gmail", ""); code != 409 || !strings.Contains(body, "ops team") {
		t.Fatalf("delete smtp in use: %d %s", code, body)
	}
	if code, body := do("DELETE", "/api/v1/channels/"+url.PathEscape("ops team"), ""); code != 409 || !strings.Contains(body, "web") {
		t.Fatalf("delete channel in use: %d %s", code, body)
	}

	// Rejected requests change nothing.
	before = state(snapshot())
	for _, tc := range []struct{ label, method, path, body string }{
		{"invalid host", "POST", "/api/v1/smtp-servers", `{"name":"x","host":"http://bad","port":25,"tls":"none"}`},
		{"tls none with username", "POST", "/api/v1/smtp-servers", `{"name":"x","host":"h.example.com","port":25,"tls":"none","username":"u"}`},
		{"duplicate smtp", "POST", "/api/v1/smtp-servers", smtp("")},
		{"channel unknown server", "POST", "/api/v1/channels", `{"name":"x","kind":"email","smtp_server":"ghost","recipients":["a@example.com"]}`},
		{"channel no recipients", "POST", "/api/v1/channels", `{"name":"x","kind":"email","smtp_server":"gmail","recipients":[]}`},
		{"channel bad email", "POST", "/api/v1/channels", `{"name":"x","kind":"email","smtp_server":"gmail","recipients":["nope"]}`},
		{"plaintext password", "POST", "/api/v1/smtp-servers", `{"name":"x","host":"h.example.com","port":25,"tls":"starttls","username":"u","password":"hunter2"}`},
	} {
		if code, body := do(tc.method, tc.path, tc.body); code < 400 || code >= 500 {
			t.Errorf("%s: status %d %s, want a 4xx", tc.label, code, body)
		}
	}
	if state(snapshot()) != before {
		t.Error("rejected requests changed the stored SMTP servers or channels")
	}

	// Delete in order.
	for _, step := range []struct{ path string }{{"/api/v1/checks/web"}, {"/api/v1/channels/" + url.PathEscape("ops team")}, {"/api/v1/smtp-servers/gmail"}} {
		if code, body := do("DELETE", step.path, ""); code != 204 {
			t.Fatalf("delete %s: %d %s", step.path, code, body)
		}
	}
	if d := snapshot(); len(d.SMTPServers) != 0 || len(d.Channels) != 0 {
		t.Fatalf("leftovers: %+v %+v", d.SMTPServers, d.Channels)
	}

	// The sealed password never came back in any response, raw or base64.
	for i, body := range bodies {
		for _, leaked := range []string{string(sealed.Data), base64.StdEncoding.EncodeToString(sealed.Data), "hunter2"} {
			if strings.Contains(body, leaked) {
				t.Fatalf("response %d leaks %q: %s", i, leaked, body)
			}
		}
	}
}
