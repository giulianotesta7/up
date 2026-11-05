package alert

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

type recordingSender struct {
	mu       sync.Mutex
	messages []Message
	err      error
	block    chan struct{}
}

func (s *recordingSender) Send(ctx context.Context, _ config.SMTPServer, _ string, m Message) error {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, m)
	return s.err
}

func (s *recordingSender) sent() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

func TestSendTestEmailIsLabelledAndSynchronous(t *testing.T) {
	st := emailStore()
	st.channel.Name = "ops-mail"
	sender := &recordingSender{}
	n := NewNotifier(Options{Store: st, Sender: sender, Hostname: "host", Logger: quiet()})
	if err := n.SendTest(context.Background(), "ops-mail"); err != nil {
		t.Fatal(err)
	}
	got := sender.sent()
	if len(got) != 1 {
		t.Fatalf("sent %d messages, want 1 before SendTest returned", len(got))
	}
	m := got[0]
	if !strings.Contains(m.Subject, "TEST") || !strings.Contains(m.Subject, "ops-mail") {
		t.Fatalf("subject %q is not labelled as a test", m.Subject)
	}
	if !strings.Contains(m.Body, "test notification") || !strings.Contains(m.Body, "no monitor data changed") {
		t.Fatalf("body %q is not labelled as a test", m.Body)
	}
	if strings.Contains(m.Subject+m.Body, "DOWN") || strings.Contains(m.Subject+m.Body, "UP again") {
		t.Fatalf("test message reads as an alert: %q %q", m.Subject, m.Body)
	}
	if m.Date.IsZero() || m.From != "s@example.com" || !reflect.DeepEqual(m.To, []string{"a@example.com"}) {
		t.Fatalf("message = %+v", m)
	}
}

func TestSendTestReturnsErrors(t *testing.T) {
	boom := errors.New("550 mailbox unavailable")
	n := NewNotifier(Options{Store: emailStore(), Sender: failingSender{boom}, Logger: quiet()})
	if err := n.SendTest(context.Background(), "mail"); !errors.Is(err, boom) {
		t.Fatalf("SendTest = %v, want the transport error", err)
	}
	n = NewNotifier(Options{Store: customStore{}, Logger: quiet()})
	if err := n.SendTest(context.Background(), "missing"); err == nil || !strings.Contains(err.Error(), `"missing" not found`) {
		t.Fatalf("SendTest = %v, want not found", err)
	}
	// A webhook channel that cannot be delivered (here: no daemon key to open
	// its sealed URL) surfaces the transport error instead of succeeding.
	n = NewNotifier(Options{Store: customStore{found: true, channel: config.Channel{Kind: config.KindWebhook}}, Logger: quiet()})
	if err := n.SendTest(context.Background(), "hook"); err == nil {
		t.Fatal("SendTest succeeded on an undeliverable webhook channel")
	}
}

// A test notification must leave every monitor table as it found it.
func TestSendTestLeavesMonitorStateUnchanged(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
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
	c := config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200, Channel: "mail", AlertAfter: 1, RecoverAfter: 1}
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, check.Result{CheckName: "api", Status: check.StatusDown, At: deliverAt, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMute(ctx, "api", deliverAt.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCert(ctx, config.Cert{Check: "api", Issuer: "CA", Observed: deliverAt, Notified: 14}); err != nil {
		t.Fatal(err)
	}
	snapshot := func() []any {
		recent, _ := s.Recent(ctx, "api", 100)
		mute, _, _ := s.Mute(ctx, "api")
		cert, _, _ := s.Cert(ctx, "api")
		checks, _ := s.Checks(ctx)
		outages, _ := s.Outages(ctx, "api", 10)
		return []any{recent, mute, cert, checks, outages}
	}
	before := snapshot()
	sender := &recordingSender{}
	n := NewNotifier(Options{Store: s, Sender: sender, Logger: quiet()})
	if err := n.SendTest(ctx, "mail"); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent()) != 1 {
		t.Fatalf("sent %d messages", len(sender.sent()))
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatalf("monitor state changed:\nbefore %+v\nafter  %+v", before, after)
	}
}

// The existing alert path keeps its subject after the test branch was added.
func TestDeliverStillSendsDownAlertAfterTestBranch(t *testing.T) {
	sender := &recordingSender{}
	n := NewNotifier(Options{Store: emailStore(), Sender: sender, Hostname: "host", Logger: quiet()})
	if err := n.Deliver(context.Background(), config.Check{Name: "api", Channel: "mail"}, check.Result{Status: check.StatusDown, At: deliverAt}); err != nil {
		t.Fatal(err)
	}
	if got := sender.sent(); len(got) != 1 || got[0].Subject != "[up] api is DOWN" {
		t.Fatalf("messages = %+v", got)
	}
}

func testRunner(t *testing.T, sender Sender) (*store.Store, TestRunner) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "smtp", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		if err := s.AddChannel(ctx, config.Channel{Name: name, Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
			t.Fatal(err)
		}
	}
	n := NewNotifier(Options{Store: s, Sender: sender, Logger: quiet()})
	return s, TestRunner{Queue: s, Notifier: n, Logger: quiet(), Interval: 10 * time.Millisecond, Timeout: time.Second}
}

func waitTest(t *testing.T, s *store.Store, channel string, want config.ChannelTestStatus) config.ChannelTest {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok, err := s.LatestChannelTest(context.Background(), channel); err == nil && ok && got.Status == want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _, _ := s.LatestChannelTest(context.Background(), channel)
	t.Fatalf("channel %s test never became %s: %+v", channel, want, got)
	return config.ChannelTest{}
}

func TestTestRunnerProcessesRequestsAndRecordsOutcome(t *testing.T) {
	sender := &recordingSender{}
	s, r := testRunner(t, sender)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	defer func() { cancel(); <-done }()
	if _, err := s.EnqueueChannelTest(ctx, "one", time.Now()); err != nil {
		t.Fatal(err)
	}
	got := waitTest(t, s, "one", config.TestOK)
	if got.Error != "" || got.Finished.IsZero() {
		t.Fatalf("ok test = %+v", got)
	}
	// A request enqueued later is picked up by polling.
	sender.err = errors.New("535 bad credentials\nsecond line")
	if _, err := s.EnqueueChannelTest(ctx, "two", time.Now()); err != nil {
		t.Fatal(err)
	}
	got = waitTest(t, s, "two", config.TestError)
	if got.Error != "535 bad credentials second line" {
		t.Fatalf("error text = %q", got.Error)
	}
}

func TestTestRunnerFailsLeftoverSendingAtStart(t *testing.T) {
	s, r := testRunner(t, &recordingSender{})
	ctx := context.Background()
	if _, err := s.EnqueueChannelTest(ctx, "one", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ClaimChannelTest(ctx, time.Now()); err != nil || !ok {
		t.Fatalf("claim = %v %v", ok, err)
	}
	c, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); r.Run(c) }()
	defer func() { cancel(); <-done }()
	if got := waitTest(t, s, "one", config.TestError); got.Error == "" {
		t.Fatalf("leftover test = %+v", got)
	}
}

func TestTestRunnerTimesOutAndStopsCleanly(t *testing.T) {
	sender := &recordingSender{block: make(chan struct{})}
	s, r := testRunner(t, sender)
	r.Timeout = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	if _, err := s.EnqueueChannelTest(ctx, "one", time.Now()); err != nil {
		t.Fatal(err)
	}
	got := waitTest(t, s, "one", config.TestError)
	if !strings.Contains(got.Error, "deadline") {
		t.Fatalf("timeout text = %q", got.Error)
	}
	// Shutdown during a send records the request instead of leaving it sending.
	r.Timeout = time.Minute
	cancel()
	<-done
	ctx2, cancel2 := context.WithCancel(context.Background())
	r.Timeout = time.Minute
	done2 := make(chan struct{})
	go func() { defer close(done2); r.Run(ctx2) }()
	if _, err := s.EnqueueChannelTest(context.Background(), "two", time.Now()); err != nil {
		t.Fatal(err)
	}
	waitTest(t, s, "two", config.TestSending)
	cancel2()
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop")
	}
	if got, _, _ := s.LatestChannelTest(context.Background(), "two"); got.Status != config.TestError {
		t.Fatalf("interrupted test = %+v", got)
	}
}
