package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
)

var heartbeatCheck = config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: 24 * time.Hour, Grace: 30 * time.Minute, Channel: "mail"}

// A heartbeat has no URL: its alerts name the schedule instead, and never
// the ping token.
func TestHeartbeatEmailNamesTheScheduleNotAURL(t *testing.T) {
	sender := notifierSender{make(chan Message, 2)}
	n := NewNotifier(Options{Store: emailStore(), Sender: sender, Hostname: "host", Location: time.UTC, Logger: quiet()})
	ctx := context.Background()
	if err := n.Deliver(ctx, heartbeatCheck, check.Result{CheckName: "backup", Status: check.StatusDown, At: deliverAt, Error: "no ping since 02:14 UTC"}); err != nil {
		t.Fatal(err)
	}
	if err := n.Deliver(ctx, heartbeatCheck, check.Ping("backup", deliverAt.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	down, up := <-sender.messages, <-sender.messages
	if want := "backup is DOWN since Jan 02 18:04:05 (UTC).\nHeartbeat: every 24h, grace 30m\nError:  no ping since 02:14 UTC\nHost:   host\n"; down.Subject != "[up] backup is DOWN" || down.Body != want {
		t.Fatalf("down = %q / %q, want %q", down.Subject, down.Body, want)
	}
	if want := "backup is UP again at Jan 02 19:04:05 (UTC).\nHeartbeat: every 24h, grace 30m\nHost:    host\n"; up.Subject != "[up] backup is UP again" || up.Body != want {
		t.Fatalf("up = %q / %q, want %q", up.Subject, up.Body, want)
	}
	for _, m := range []Message{down, up} {
		if strings.Contains(m.Body, "URL") || strings.Contains(m.Body, "Latency") || strings.Contains(m.Body, "uphb_") {
			t.Fatalf("heartbeat mail shows a URL, latency or token: %q", m.Body)
		}
	}
}

func TestHeartbeatWebhookAddsTheSchedule(t *testing.T) {
	n, _, requests := hookNotifier(t, http.StatusOK, "", "", nil)
	c := heartbeatCheck
	c.Channel = "hook"
	if err := n.Deliver(context.Background(), c, check.Fail("backup", deliverAt)); err != nil {
		t.Fatal(err)
	}
	body := (<-requests).body
	raw, _ := json.Marshal(body)
	monitor := body["monitor"].(map[string]any)
	if monitor["name"] != "backup" || monitor["type"] != "heartbeat" || monitor["period_ms"] != float64(24*time.Hour/time.Millisecond) || monitor["grace_ms"] != float64(30*time.Minute/time.Millisecond) {
		t.Fatalf("monitor = %v", monitor)
	}
	if body["event"] != "monitor.down" || body["result"].(map[string]any)["reason"] != "reported failure" || strings.Contains(string(raw), "uphb_") {
		t.Fatalf("body = %s", raw)
	}
}

var cronCheck = config.Check{Name: "backup", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "0 2 * * 1-5", Grace: 30 * time.Minute, Channel: "mail"}

func TestCronHeartbeatEmailNamesTheCron(t *testing.T) {
	sender := notifierSender{make(chan Message, 1)}
	n := NewNotifier(Options{Store: emailStore(), Sender: sender, Hostname: "host", Location: time.UTC, Logger: quiet()})
	if err := n.Deliver(context.Background(), cronCheck, check.Result{CheckName: "backup", Status: check.StatusDown, At: deliverAt, Error: "no ping for the 02:00 UTC run"}); err != nil {
		t.Fatal(err)
	}
	down := <-sender.messages
	if want := "backup is DOWN since Jan 02 18:04:05 (UTC).\nHeartbeat: cron 0 2 * * 1-5, grace 30m\nError:  no ping for the 02:00 UTC run\nHost:   host\n"; down.Body != want {
		t.Fatalf("down = %q, want %q", down.Body, want)
	}
}

func TestCronHeartbeatWebhookAddsTheCron(t *testing.T) {
	n, _, requests := hookNotifier(t, http.StatusOK, "", "", nil)
	c := cronCheck
	c.Channel = "hook"
	if err := n.Deliver(context.Background(), c, check.Fail("backup", deliverAt)); err != nil {
		t.Fatal(err)
	}
	monitor := (<-requests).body["monitor"].(map[string]any)
	if monitor["cron"] != "0 2 * * 1-5" || monitor["grace_ms"] != float64(30*time.Minute/time.Millisecond) {
		t.Fatalf("monitor = %v", monitor)
	}
	if _, ok := monitor["period_ms"]; ok {
		t.Fatalf("a cron heartbeat's webhook has period_ms: %v", monitor)
	}
}
