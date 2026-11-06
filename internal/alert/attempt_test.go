package alert

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

type attemptLog struct {
	mu   sync.Mutex
	list []store.Attempt
	fail error
}

func (l *attemptLog) RecordAttempt(_ context.Context, a store.Attempt) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail != nil {
		return l.fail
	}
	l.list = append(l.list, a)
	return nil
}

func (l *attemptLog) all() []store.Attempt {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]store.Attempt(nil), l.list...)
}

var attemptNow = time.Date(2025, 1, 2, 18, 5, 0, 0, time.UTC)

func recording(n *Notifier) *attemptLog {
	log := &attemptLog{}
	n.opts.Attempts, n.opts.Now = log, func() time.Time { return attemptNow }
	return log
}

var downResult = check.Result{Status: check.StatusDown, At: deliverAt, Error: "boom"}

func TestSentEmailIsRecordedPerEvent(t *testing.T) {
	sender := notifierSender{make(chan Message, 3)}
	n := NewNotifier(Options{Store: emailStore(), Sender: sender, Hostname: "host", Location: time.UTC, Logger: quiet()})
	log := recording(n)
	c := config.Check{Name: "api", URL: "https://example.com", Channel: "mail"}
	ctx := context.Background()
	if err := n.Deliver(ctx, c, downResult); err != nil {
		t.Fatal(err)
	}
	if err := n.Deliver(ctx, c, check.Result{Status: check.StatusUp, At: deliverAt}); err != nil {
		t.Fatal(err)
	}
	if err := n.DeliverCert(ctx, c, config.Cert{Check: "api", Observed: deliverAt}, 5*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	want := []store.Attempt{
		{At: attemptNow, Monitor: "api", Channel: "mail", Event: store.EventDown, Outcome: store.OutcomeSent},
		{At: attemptNow, Monitor: "api", Channel: "mail", Event: store.EventRecovered, Outcome: store.OutcomeSent},
		{At: attemptNow, Monitor: "api", Channel: "mail", Event: store.EventCert, Outcome: store.OutcomeSent},
	}
	got := log.all()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("attempts = %+v, want %+v", got, want)
	}
}

func TestQueuedAlertsAreRecordedToo(t *testing.T) {
	sender := notifierSender{make(chan Message, 1)}
	n := NewNotifier(Options{Store: emailStore(), Sender: sender, Location: time.UTC, Logger: quiet()})
	log := recording(n)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)
	n.Notify(config.Check{Name: "api", Channel: "mail"}, downResult)
	<-sender.messages
	deadline := time.Now().Add(5 * time.Second)
	for len(log.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := log.all(); len(got) != 1 || got[0].Outcome != store.OutcomeSent || got[0].Event != store.EventDown {
		t.Fatalf("attempts = %+v", got)
	}
}

func TestChannelTestIsNotRecorded(t *testing.T) {
	sender := notifierSender{make(chan Message, 1)}
	n := NewNotifier(Options{Store: emailStore(), Sender: sender, Location: time.UTC, Logger: quiet()})
	log := recording(n)
	if err := n.SendTest(context.Background(), "mail"); err != nil {
		t.Fatal(err)
	}
	if got := log.all(); len(got) != 0 || len(sender.messages) != 1 {
		t.Fatalf("test message recorded %+v (sent %d)", got, len(sender.messages))
	}
}

type failSender struct{ err error }

func (s failSender) Send(context.Context, config.SMTPServer, string, Message) error { return s.err }

func TestFailedEmailRecordsSanitizedReason(t *testing.T) {
	refused := fmt.Errorf("alert: send via 10.0.0.9:587: %w", &net.OpError{Op: "dial", Net: "tcp", Err: &net.OpError{Op: "connect", Err: syscall.ECONNREFUSED}})
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"rejected": {fmt.Errorf("alert: send via mail.example.com:587: %w", &textproto.Error{Code: 550, Msg: "mailbox unavailable"}), "mail server: 550 mailbox unavailable"},
		"refused":  {refused, "mail server: connection refused"},
		"timeout":  {fmt.Errorf("alert: send via mail.example.com:587: %w", context.DeadlineExceeded), "mail server: timed out"},
	} {
		t.Run(name, func(t *testing.T) {
			n := NewNotifier(Options{Store: emailStore(), Sender: failSender{tc.err}, Location: time.UTC, Logger: quiet()})
			log := recording(n)
			err := n.Deliver(context.Background(), config.Check{Name: "api", Channel: "mail"}, downResult)
			if err == nil {
				t.Fatal("Deliver succeeded")
			}
			got := log.all()
			if len(got) != 1 || got[0].Outcome != store.OutcomeFailed || got[0].Reason != tc.want {
				t.Fatalf("attempts = %+v, want one failed with %q", got, tc.want)
			}
		})
	}
}

// A failing login whose server echoes the password back never stores it.
func TestSMTPLoginFailureNeverLeaksPassword(t *testing.T) {
	key, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := secret.Seal(key.PublicKey(), "hunter2-PASSWORD")
	if err != nil {
		t.Fatal(err)
	}
	s := customStore{found: true, channel: config.Channel{Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}},
		server: config.SMTPServer{Host: "mail.example.com", Port: 587, Username: "admin-USER", Password: sealed, Sender: "s@example.com"}}
	login := &textproto.Error{Code: 535, Msg: "5.7.8 Authentication failed for admin-USER with hunter2-PASSWORD"}
	n := NewNotifier(Options{Store: s, Key: key, Sender: failSender{fmt.Errorf("alert: send via mail.example.com:587: %w", login)}, Location: time.UTC, Logger: quiet()})
	log := recording(n)
	if err := n.Deliver(context.Background(), config.Check{Name: "api", Channel: "mail"}, downResult); err == nil {
		t.Fatal("Deliver succeeded")
	}
	got := log.all()
	if len(got) != 1 || got[0].Outcome != store.OutcomeFailed || !strings.HasPrefix(got[0].Reason, "mail server: 535 ") {
		t.Fatalf("attempts = %+v", got)
	}
	if strings.Contains(got[0].Reason, "hunter2") {
		t.Fatalf("reason leaks the password: %q", got[0].Reason)
	}
}

func TestWebhookFailureRecordsOriginAndStatusOnly(t *testing.T) {
	n, srv, _ := hookNotifier(t, 502, "X-Api-Key", "AUTHVALUE", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(502)
		_, _ = fmt.Fprint(w, r.URL.String(), r.Header.Get("X-Api-Key"))
	})
	log := recording(n)
	n.opts.Store = customStore{found: true, channel: func() config.Channel {
		ch, _, _ := n.opts.Store.Channel(context.Background(), "hook")
		return ch
	}()}
	if err := n.Deliver(context.Background(), config.Check{Name: "api", Channel: "hook"}, downResult); err == nil {
		t.Fatal("Deliver succeeded")
	}
	got := log.all()
	want := "webhook " + srv.URL + ": answered 502 Bad Gateway"
	if len(got) != 1 || got[0].Outcome != store.OutcomeFailed || got[0].Reason != want {
		t.Fatalf("attempts = %+v, want reason %q", got, want)
	}
	for _, leak := range []string{"SECRETPATH", "SECRETQUERY", "AUTHVALUE", "hooks/"} {
		if strings.Contains(got[0].Reason, leak) {
			t.Fatalf("reason leaks %q: %q", leak, got[0].Reason)
		}
	}
}

func TestWebhookConnectionFailureAndSuccessAreRecorded(t *testing.T) {
	n, srv, _ := hookNotifier(t, 200, "Authorization", "AUTHVALUE", nil)
	log := recording(n)
	c := config.Check{Name: "api", Channel: "hook"}
	if err := n.Deliver(context.Background(), c, downResult); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if err := n.Deliver(context.Background(), c, downResult); err == nil {
		t.Fatal("Deliver succeeded against a closed server")
	}
	got := log.all()
	if len(got) != 2 || got[0].Outcome != store.OutcomeSent || got[0].Reason != "" || got[1].Outcome != store.OutcomeFailed {
		t.Fatalf("attempts = %+v", got)
	}
	for _, leak := range []string{"SECRETPATH", "SECRETQUERY", "AUTHVALUE", "127.0.0.1:"} {
		if strings.Contains(strings.ReplaceAll(got[1].Reason, srv.URL, "ORIGIN"), leak) {
			t.Fatalf("reason leaks %q: %q", leak, got[1].Reason)
		}
	}
	if !strings.HasPrefix(got[1].Reason, "webhook "+srv.URL+": ") {
		t.Fatalf("reason = %q, want it to name the origin", got[1].Reason)
	}
}

func TestMissingChannelIsRecordedAsFailed(t *testing.T) {
	n := NewNotifier(Options{Store: customStore{}, Location: time.UTC, Logger: quiet()})
	log := recording(n)
	if err := n.Deliver(context.Background(), config.Check{Name: "api", Channel: "gone"}, downResult); err == nil {
		t.Fatal("Deliver succeeded")
	}
	got := log.all()
	if len(got) != 1 || got[0].Channel != "gone" || got[0].Outcome != store.OutcomeFailed || got[0].Reason != `alert channel "gone" not found` {
		t.Fatalf("attempts = %+v", got)
	}
}

// A recorder that fails never changes the delivery result.
func TestRecorderFailureDoesNotChangeDelivery(t *testing.T) {
	sender := notifierSender{make(chan Message, 1)}
	n := NewNotifier(Options{Store: emailStore(), Sender: sender, Location: time.UTC, Logger: quiet()})
	log := recording(n)
	log.fail = errors.New("database is locked")
	if err := n.Deliver(context.Background(), config.Check{Name: "api", Channel: "mail"}, downResult); err != nil {
		t.Fatalf("Deliver = %v, want nil", err)
	}
	if len(sender.messages) != 1 {
		t.Fatal("message not sent")
	}
}

// With the real store as Options.Store, attempts are recorded without any
// extra wiring, and a channel test still records nothing.
func TestRealStoreRecordsAttemptsByDefault(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir() + "/up.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "smtp", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, config.Channel{Name: "mail", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
		t.Fatal(err)
	}
	c := config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200, Channel: "mail"}
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	refuse := fmt.Errorf("alert: send via localhost:25: %w", &textproto.Error{Code: 550, Msg: "mailbox unavailable"})
	n := NewNotifier(Options{Store: s, Sender: failSender{refuse}, Logger: quiet()})
	if err := n.SendTest(ctx, "mail"); err == nil {
		t.Fatal("test send succeeded")
	}
	if got, _ := s.Attempts(ctx, store.AttemptFilter{}); len(got) != 0 {
		t.Fatalf("channel test recorded %+v", got)
	}
	if err := n.Deliver(ctx, c, downResult); err == nil {
		t.Fatal("Deliver succeeded")
	}
	got, err := s.Attempts(ctx, store.AttemptFilter{})
	if err != nil || len(got) != 1 || got[0].Monitor != "api" || got[0].Channel != "mail" || got[0].Outcome != store.OutcomeFailed || got[0].Reason != "mail server: 550 mailbox unavailable" || time.Since(got[0].At) > time.Minute {
		t.Fatalf("attempts = %+v, %v", got, err)
	}
}
