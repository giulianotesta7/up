package alert

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

type notifierStore struct{}

func (notifierStore) Channel(context.Context, string) (config.Channel, bool, error) {
	return config.Channel{Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}, true, nil
}
func (notifierStore) SMTPServer(context.Context, string) (config.SMTPServer, bool, error) {
	return config.SMTPServer{Sender: "sender@example.com"}, true, nil
}
func (notifierStore) Outages(context.Context, string, int) ([]store.Outage, error) { return nil, nil }

type notifierSender struct{ messages chan Message }

func (s notifierSender) Send(_ context.Context, _ config.SMTPServer, _ string, m Message) error {
	s.messages <- m
	return nil
}

type customStore struct {
	notifierStore
	channel config.Channel
	server  config.SMTPServer
	outages []store.Outage
	found   bool
}

func (s customStore) Channel(context.Context, string) (config.Channel, bool, error) {
	return s.channel, s.found, nil
}
func (s customStore) SMTPServer(context.Context, string) (config.SMTPServer, bool, error) {
	return s.server, true, nil
}
func (s customStore) Outages(context.Context, string, int) ([]store.Outage, error) {
	return s.outages, nil
}

func TestNotifierDownMessage(t *testing.T) {
	sender := notifierSender{make(chan Message, 1)}
	n := NewNotifier(Options{Store: notifierStore{}, Sender: sender, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Hostname: "host", Location: time.FixedZone("-03", -3*3600)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)
	at := time.Date(2025, 1, 2, 18, 4, 5, 0, time.UTC)
	n.Notify(config.Check{Name: "api", URL: "https://example.com", Channel: "mail"}, check.Result{Status: check.StatusDown, At: at, Error: "failure"})
	select {
	case m := <-sender.messages:
		if m.Subject != "[up] api is DOWN" || m.Body != "api is DOWN since Jan 02 15:04:05 (-03).\nURL:    https://example.com\nError:  failure\nHost:   host\n" {
			t.Fatalf("message: %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
}

func TestNotifierRecoveryAndOrder(t *testing.T) {
	at := time.Date(2025, 1, 2, 18, 4, 5, 0, time.UTC)
	s := customStore{found: true, channel: config.Channel{Kind: config.KindEmail, Recipients: []string{"a@example.com"}}, server: config.SMTPServer{Sender: "s@example.com"}, outages: []store.Outage{{Start: at.Add(-328 * time.Second), End: at}}}
	sender := notifierSender{make(chan Message, 3)}
	n := NewNotifier(Options{Store: s, Sender: sender, Hostname: "host", Location: time.FixedZone("-03", -3*3600), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	c := config.Check{Name: "api", URL: "https://example.com", Channel: "mail"}
	n.Notify(c, check.Result{Status: check.StatusDown, At: at})
	n.Notify(c, check.Result{Status: check.StatusUp, At: at, Latency: 1500 * time.Microsecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)
	for i := 0; i < 2; i++ {
		select {
		case m := <-sender.messages:
			if i == 0 && m.Subject != "[up] api is DOWN" {
				t.Fatal(m.Subject)
			}
			if i == 1 && (m.Subject != "[up] api is UP again" || m.Body != "api is UP again at Jan 02 15:04:05 (-03), after 5m28s down.\nURL:     https://example.com\nLatency: 2ms\nHost:    host\n") {
				t.Fatalf("message: %+v", m)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out")
		}
	}
}

func TestNotifierRejectsAndDrops(t *testing.T) {
	key, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := secret.Seal(other.PublicKey(), "password")
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	s := customStore{found: true, channel: config.Channel{Kind: config.KindEmail, SMTPServer: "smtp"}, server: config.SMTPServer{Password: sealed}}
	sender := notifierSender{make(chan Message, 1)}
	n := NewNotifier(Options{Store: s, Key: key, Sender: sender, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	e := event{check: config.Check{Name: "api", Channel: "mail"}, result: check.Result{Status: check.StatusDown}}
	n.send(context.Background(), e)
	if !strings.Contains(logs.String(), "SMTP server password was sealed to another key; enter it again in the TUI") || len(sender.messages) != 0 || strings.Contains(logs.String(), "password=password") {
		t.Fatal(logs.String())
	}
	logs.Reset()
	n.opts.Store = customStore{}
	n.send(context.Background(), e)
	if !strings.Contains(logs.String(), "alert channel not found") {
		t.Fatal(logs.String())
	}
	logs.Reset()
	for i := 0; i < 65; i++ {
		n.Notify(e.check, e.result)
	}
	if !strings.Contains(logs.String(), "alert dropped: queue full") {
		t.Fatal(logs.String())
	}
}

func TestNotifierRecoveryWithoutOutageAndFullQueueDoesNotBlock(t *testing.T) {
	at := time.Date(2025, 1, 2, 18, 4, 5, 0, time.UTC)
	s := customStore{found: true, channel: config.Channel{Kind: config.KindEmail, Recipients: []string{"a@example.com"}}, server: config.SMTPServer{Sender: "s@example.com"}}
	sender := notifierSender{make(chan Message, 1)}
	n := NewNotifier(Options{Store: s, Sender: sender, Hostname: "host", Location: time.FixedZone("-03", -3*3600), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	c := config.Check{Name: "api", URL: "https://example.com", Channel: "mail"}
	n.send(context.Background(), event{check: c, result: check.Result{Status: check.StatusUp, At: at, Latency: 84 * time.Millisecond}})
	if m := <-sender.messages; m.Body != "api is UP again at Jan 02 15:04:05 (-03).\nURL:     https://example.com\nLatency: 84ms\nHost:    host\n" {
		t.Fatalf("body = %q", m.Body)
	}

	start := time.Now()
	for range 1000 {
		n.Notify(c, check.Result{Status: check.StatusDown, At: at})
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("1000 Notify calls with nobody draining took %v, want non-blocking", elapsed)
	}
}
