package alert

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

func TestTCPMonitorEmailsShowHostAndPort(t *testing.T) {
	sender := notifierSender{make(chan Message, 2)}
	n := NewNotifier(Options{Store: notifierStore{}, Sender: sender, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Hostname: "host", Location: time.UTC})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go n.Run(ctx)
	c := config.Check{Name: "db", Kind: config.CheckTCP, Host: "db.example.com", Port: 5432, Channel: "mail"}
	at := time.Date(2025, 1, 2, 18, 4, 5, 0, time.UTC)
	n.Notify(c, check.Result{Status: check.StatusDown, At: at, Error: "connection refused"})
	n.Notify(c, check.Result{Status: check.StatusUp, At: at.Add(time.Minute)})
	for _, want := range []string{"[up] db is DOWN", "[up] db is UP again"} {
		select {
		case m := <-sender.messages:
			if m.Subject != want || !strings.Contains(m.Body, "URL:") || !strings.Contains(m.Body, "db.example.com:5432\n") {
				t.Fatalf("message: %+v", m)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no %q message", want)
		}
	}
}
