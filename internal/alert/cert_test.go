package alert

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

func TestCertificateQueueOrderAndMinuteFormatting(t *testing.T) {
	sender := notifierSender{make(chan Message, 3)}
	n := NewNotifier(Options{Store: notifierStore{}, Sender: sender, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.FixedZone("X", 3600)})
	c := config.Check{Name: "api", URL: "https://example.com", Channel: "mail"}
	cert := config.Cert{Host: "example.com", Issuer: "CA", NotAfter: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), Notified: 3}
	n.Notify(c, check.Result{Status: check.StatusDown, At: cert.NotAfter})
	n.NotifyCert(c, cert, 30*time.Minute)
	n.Notify(c, check.Result{Status: check.StatusUp, At: cert.NotAfter})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)
	for i := 0; i < 3; i++ {
		select {
		case msg := <-sender.messages:
			switch i {
			case 0:
				if msg.Subject != "[up] api is DOWN" {
					t.Fatal(msg.Subject)
				}
			case 1:
				if msg.Subject != "[up] api: TLS certificate expires in 1 hour" || msg.Body != "The certificate served by example.com expires on 2026-11-02 10:00 UTC (30 minutes from now).\n\n  Monitor   api (https://example.com)\n  Issuer    CA\n\nIf the certificate is renewed automatically, up will\ndetect the new one and stop warning.\n" {
					t.Fatalf("message: %+v", msg)
				}
			case 2:
				if msg.Subject != "[up] api is UP again" {
					t.Fatal(msg.Subject)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing message")
		}
	}
}

func TestNotifierCertificateMessages(t *testing.T) {
	sender := notifierSender{make(chan Message, 3)}
	n := NewNotifier(Options{Store: notifierStore{}, Sender: sender, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: time.UTC})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)
	c := config.Check{Name: "api", URL: "https://example.com", Channel: "mail"}
	cert := config.Cert{Host: "example.com", Issuer: "Example CA", NotAfter: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), Notified: 7}
	n.NotifyCert(c, cert, 6*24*time.Hour+21*time.Hour)
	select {
	case m := <-sender.messages:
		want := "The certificate served by example.com expires on 2026-11-02 10:00 UTC (6 days 21 hours from now).\n\n  Monitor   api (https://example.com)\n  Issuer    Example CA\n  Next warning at 3 days.\n\nIf the certificate is renewed automatically, up will\ndetect the new one and stop warning.\n"
		if m.Subject != "[up] api: TLS certificate expires in 6 days" || m.Body != want {
			t.Fatalf("message = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no certificate message")
	}
	cert.Notified = 3
	n.NotifyCert(c, cert, 90*time.Minute)
	select {
	case m := <-sender.messages:
		if m.Subject != "[up] api: TLS certificate expires in 1 hour" || m.Body != "The certificate served by example.com expires on 2026-11-02 10:00 UTC (1 hour from now).\n\n  Monitor   api (https://example.com)\n  Issuer    Example CA\n\nIf the certificate is renewed automatically, up will\ndetect the new one and stop warning.\n" {
			t.Fatalf("message = %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no certificate message")
	}
}
