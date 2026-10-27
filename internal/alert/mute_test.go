package alert

import (
	"context"
	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
	"strings"
	"testing"
	"time"
)

func TestLateDownUsesOutageStart(t *testing.T) {
	at := time.Date(2025, 1, 2, 18, 4, 5, 0, time.UTC)
	s := customStore{found: true, channel: config.Channel{Kind: config.KindEmail}, outages: []store.Outage{{Start: at.Add(-time.Hour), Ongoing: true}}}
	sender := notifierSender{make(chan Message, 1)}
	n := NewNotifier(Options{Store: s, Sender: sender, Location: time.UTC})
	n.send(context.Background(), event{check: config.Check{Name: "api"}, result: check.Result{Status: check.StatusDown, At: at}})
	if m := <-sender.messages; !strings.Contains(m.Body, "DOWN since Jan 02 17:04:05 (UTC)") {
		t.Fatalf("body: %s", m.Body)
	}
}
