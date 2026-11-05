package alert

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

type failingSender struct{ err error }

func (s failingSender) Send(context.Context, config.SMTPServer, string, Message) error { return s.err }

type errStore struct {
	customStore
	channelErr error
}

func (s errStore) Channel(context.Context, string) (config.Channel, bool, error) {
	return config.Channel{}, false, s.channelErr
}

var deliverAt = time.Date(2025, 1, 2, 18, 4, 5, 0, time.UTC)

func emailStore() customStore {
	return customStore{found: true, channel: config.Channel{Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}, server: config.SMTPServer{Sender: "s@example.com"}, outages: []store.Outage{}}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestDeliverSendsSynchronouslyAndReturnsNil(t *testing.T) {
	sender := notifierSender{make(chan Message, 2)}
	n := NewNotifier(Options{Store: emailStore(), Sender: sender, Hostname: "host", Location: time.UTC, Logger: quiet()})
	c := config.Check{Name: "api", URL: "https://example.com", Channel: "mail"}
	if err := n.Deliver(context.Background(), c, check.Result{Status: check.StatusDown, At: deliverAt, Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	// Nothing is queued or run in the background: the message is already there.
	select {
	case m := <-sender.messages:
		if m.Subject != "[up] api is DOWN" {
			t.Fatalf("subject = %q", m.Subject)
		}
	default:
		t.Fatal("Deliver returned before sending")
	}
	cert := config.Cert{Check: "api", Issuer: "CA", Observed: deliverAt, Notified: 14}
	if err := n.DeliverCert(context.Background(), c, cert, 5*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if m := <-sender.messages; !strings.Contains(m.Subject, "TLS certificate expires in 5 days") {
		t.Fatalf("cert subject = %q", m.Subject)
	}
}

func TestDeliverReturnsEveryFailure(t *testing.T) {
	key, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := secret.LoadOrCreate(t.TempDir() + "/key")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := secret.Seal(other.PublicKey(), "password")
	if err != nil {
		t.Fatal(err)
	}
	c := config.Check{Name: "api", Channel: "mail"}
	r := check.Result{Status: check.StatusDown, At: deliverAt}
	sendErr := errors.New("550 mailbox unavailable")
	tests := []struct {
		name  string
		opts  Options
		check config.Check
		want  string
	}{
		{"channel read fails", Options{Store: errStore{channelErr: errors.New("disk")}}, c, "reading alert channel"},
		{"channel missing", Options{Store: customStore{}}, c, `alert channel "mail" not found`},
		{"unsupported kind", Options{Store: customStore{found: true, channel: config.Channel{Kind: "sms"}}}, c, `unsupported alert channel kind "sms"`},
		{"SMTP server missing", Options{Store: func() Store { s := emailStore(); s.server = config.SMTPServer{}; return noServer{s} }()}, c, `SMTP server "smtp" not found`},
		{"password without key", Options{Store: customStore{found: true, channel: emailStore().channel, server: config.SMTPServer{Password: foreign}}}, c, "password cannot be opened: no encryption key"},
		{"password sealed to another key", Options{Store: customStore{found: true, channel: emailStore().channel, server: config.SMTPServer{Password: foreign}}, Key: key}, c, "sealed to another key"},
		{"transport rejects", Options{Store: emailStore(), Sender: failingSender{sendErr}}, c, "550 mailbox unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.Logger = quiet()
			n := NewNotifier(tt.opts)
			err := n.Deliver(context.Background(), tt.check, r)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Deliver() = %v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "password") && strings.Contains(err.Error(), "password=") {
				t.Fatalf("error leaks a secret: %v", err)
			}
		})
	}
	// The transport error stays inspectable.
	n := NewNotifier(Options{Store: emailStore(), Sender: failingSender{sendErr}, Logger: quiet()})
	if err := n.Deliver(context.Background(), c, r); !errors.Is(err, sendErr) {
		t.Fatalf("Deliver() = %v, want it to wrap the transport error", err)
	}
}

type noServer struct{ customStore }

func (noServer) SMTPServer(context.Context, string) (config.SMTPServer, bool, error) {
	return config.SMTPServer{}, false, nil
}

// The queue goes through the same delivery call and still only logs.
func TestQueueUsesTheDeliveryCallAndLogsFailures(t *testing.T) {
	var logs bytes.Buffer
	n := NewNotifier(Options{Store: emailStore(), Sender: failingSender{errors.New("refused")}, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	n.send(context.Background(), event{check: config.Check{Name: "api", Channel: "mail"}, result: check.Result{Status: check.StatusDown, At: deliverAt}})
	if !strings.Contains(logs.String(), "sending alert failed") || !strings.Contains(logs.String(), "refused") {
		t.Fatalf("logs = %q", logs.String())
	}
}
