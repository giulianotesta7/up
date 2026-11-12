package alert

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
)

// attemptDB is a real store with the monitor "api", recording the attempts.
func attemptDB(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir() + "/up.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}
	if err := s.AddCheck(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return s
}

// storedReason is the reason of the only stored attempt, which must be failed.
func storedReason(t *testing.T, s *store.Store) string {
	t.Helper()
	got, err := s.Attempts(context.Background(), store.AttemptFilter{})
	if err != nil || len(got) != 1 || got[0].Outcome != store.OutcomeFailed {
		t.Fatalf("attempts = %+v, %v, want one failed", got, err)
	}
	return got[0].Reason
}

// loginFailure delivers through a mail server whose login fails with reply,
// as user with password, and returns the stored reason.
func loginFailure(t *testing.T, user, password, reply string) string {
	t.Helper()
	key, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := secret.Seal(key.PublicKey(), password)
	if err != nil {
		t.Fatal(err)
	}
	st := customStore{found: true, channel: config.Channel{Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}},
		server: config.SMTPServer{Host: "mail.example.com", Port: 587, Username: user, Password: sealed, Sender: "s@example.com"}}
	login := &textproto.Error{Code: 535, Msg: reply}
	db := attemptDB(t)
	n := NewNotifier(Options{Store: st, Attempts: db, Key: key, Sender: failSender{fmt.Errorf("alert: send via mail.example.com:587: %w", login)}, Location: time.UTC, Logger: quiet()})
	if err := n.Deliver(context.Background(), config.Check{Name: "api", Channel: "mail"}, downResult); err == nil {
		t.Fatal("Deliver succeeded")
	}
	return storedReason(t, db)
}

func TestLoginEchoAcrossLinesNeverStoresThePassword(t *testing.T) {
	reason := loginFailure(t, "admin", "correct horse", "5.7.8 got correct\nhorse, rejected")
	if strings.Contains(reason, "correct") || strings.Contains(reason, "horse") {
		t.Fatalf("reason leaks the password: %q", reason)
	}
	if want := "mail server: 535 5.7.8 got [hidden], rejected"; reason != want {
		t.Fatalf("reason = %q, want %q", reason, want)
	}
}

func TestLoginEchoOfEncodedCredentialsNeverStoresThem(t *testing.T) {
	plain := "\x00admin\x00correct horse"
	for name, echo := range map[string]string{
		"plain":             base64.StdEncoding.EncodeToString([]byte(plain)),
		"plain unpadded":    base64.RawStdEncoding.EncodeToString([]byte(plain)),
		"password":          base64.StdEncoding.EncodeToString([]byte("correct horse")),
		"password unpadded": base64.RawStdEncoding.EncodeToString([]byte("correct horse")),
	} {
		t.Run(name, func(t *testing.T) {
			reason := loginFailure(t, "admin", "correct horse", "5.7.8 bad credentials "+echo)
			if strings.Contains(reason, base64.RawStdEncoding.EncodeToString([]byte(plain))[:12]) || strings.Contains(reason, base64.RawStdEncoding.EncodeToString([]byte("correct horse"))[:12]) {
				t.Fatalf("reason leaks the encoded credentials: %q", reason)
			}
			if want := "mail server: 535 5.7.8 bad credentials [hidden]"; reason != want {
				t.Fatalf("reason = %q, want %q", reason, want)
			}
		})
	}
}

// A short password is hidden where it stands alone, never inside ordinary
// words, where the marks would garble the reason and show where it occurs.
func TestShortPasswordIsHiddenOnlyWhereItStandsAlone(t *testing.T) {
	if reason := loginFailure(t, "admin", "ab", "5.7.8 mailbox unavailable"); reason != "mail server: 535 5.7.8 mailbox unavailable" {
		t.Fatalf("reason = %q", reason)
	}
	if reason := loginFailure(t, "admin", "ab", "5.7.8 password ab rejected (ab)"); reason != "mail server: 535 5.7.8 password [hidden] rejected ([hidden])" {
		t.Fatalf("reason = %q", reason)
	}
}

// hookTo is a notifier for a webhook channel posting to target on srv, with
// an auth header, recording into a real store.
func hookTo(t *testing.T, srv *httptest.Server, target string) (*Notifier, *store.Store) {
	t.Helper()
	key, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
	if err != nil {
		t.Fatal(err)
	}
	sealedURL, err := secret.Seal(key.PublicKey(), target)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := secret.Seal(key.PublicKey(), "Bearer AUTHVALUE")
	if err != nil {
		t.Fatal(err)
	}
	ch := config.Channel{Kind: config.KindWebhook, WebhookOrigin: srv.URL, WebhookURL: sealedURL, WebhookAuthName: "Authorization", WebhookAuth: auth}
	db := attemptDB(t)
	return NewNotifier(Options{Store: customStore{found: true, channel: ch}, Attempts: db, Key: key, Logger: quiet(), Location: time.UTC}), db
}

func TestWebhookFailureReasonsCarryNoSecret(t *testing.T) {
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/SECRETLOCATION?token=SECRETLOCQUERY", http.StatusFound)
	}))
	defer redirect.Close()
	tlsSrv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	tlsSrv.Config.ErrorLog = log.New(io.Discard, "", 0)
	tlsSrv.StartTLS()
	defer tlsSrv.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	for name, tc := range map[string]struct {
		srv     *httptest.Server
		timeout time.Duration
		want    string
	}{
		"redirect": {redirect, time.Minute, "answered 302 Found"},
		"tls":      {tlsSrv, time.Minute, "TLS certificate was not accepted"},
		"timeout":  {slow, 100 * time.Millisecond, "request timed out"},
	} {
		t.Run(name, func(t *testing.T) {
			n, db := hookTo(t, tc.srv, tc.srv.URL+"/hooks/SECRETPATH?token=SECRETQUERY")
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			if err := n.Deliver(ctx, config.Check{Name: "api", Channel: "hook"}, downResult); err == nil {
				t.Fatal("Deliver succeeded")
			}
			reason := storedReason(t, db)
			if want := "webhook " + tc.srv.URL + ": " + tc.want; reason != want {
				t.Fatalf("reason = %q, want %q", reason, want)
			}
			for _, leak := range []string{"SECRETPATH", "SECRETQUERY", "AUTHVALUE", "Bearer", "hooks/", "SECRETLOCATION", "SECRETLOCQUERY", "elsewhere"} {
				if strings.Contains(reason, leak) {
					t.Fatalf("reason leaks %q: %q", leak, reason)
				}
			}
		})
	}
}
