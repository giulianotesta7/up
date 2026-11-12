package monitor

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
)

// Without permission to send pings a ping monitor is never run: it records
// nothing and sends no alert, even one owed from before, while the other
// monitors keep running.
func TestPingMonitorsWaitWhenTheDaemonCannotPing(t *testing.T) {
	s := openStore(t)
	bg := context.Background()
	if err := s.AddSMTPServer(bg, config.SMTPServer{Name: "smtp", Host: "example.com", Port: 25, TLS: config.TLSNone, Sender: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(bg, config.Channel{Name: "mail", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
		t.Fatal(err)
	}
	router := config.Check{Name: "router", Kind: config.CheckPing, Host: "192.168.1.1", Interval: 10 * time.Millisecond, Timeout: 5 * time.Millisecond, Channel: "mail"}
	nas := config.Check{Name: "nas", Kind: config.CheckDNS, Domain: "nas.home.arpa", Record: "A", Interval: 10 * time.Millisecond, Timeout: 5 * time.Millisecond, Channel: "mail"}
	for _, c := range []config.Check{router, nas} {
		if err := s.AddCheck(bg, c); err != nil {
			t.Fatal(err)
		}
	}
	// A down result from before the daemon lost permission is owed an alert.
	if err := s.Record(bg, check.Result{CheckName: "router", At: time.Now().Add(-time.Minute), Status: check.StatusDown, Error: "no reply from 192.168.1.1 in 5ms"}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{calls: map[string]int{}}
	a := &recordingAlerter{}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Store: s, Runner: runner, Alerter: a, Pings: config.PingDenied, HeartbeatInterval: time.Hour,
			DiscoveryInterval: 5 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	waitFor(t, "dns results", func() bool { rows, _ := s.Recent(bg, "nas", 10); return len(rows) >= 3 })
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	calls := runner.calls["router"]
	runner.mu.Unlock()
	if calls != 0 {
		t.Fatalf("the ping monitor ran %d times without permission", calls)
	}
	if rows, _ := s.Recent(bg, "router", 10); len(rows) != 1 {
		t.Fatalf("router results = %+v, want only the one from before", rows)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, name := range a.names {
		if name == "router" {
			t.Fatalf("alerts %v include the ping monitor", a.names)
		}
	}
}

// With permission, or when it is unknown, ping monitors run like any other.
func TestPingMonitorsRunWhenTheDaemonCanPing(t *testing.T) {
	for _, p := range []config.PingPermission{config.PingAllowed, config.PingUnknown} {
		s := openStore(t)
		bg := context.Background()
		router := config.Check{Name: "router", Kind: config.CheckPing, Host: "192.168.1.1", Interval: 10 * time.Millisecond, Timeout: 5 * time.Millisecond}
		if err := s.AddCheck(bg, router); err != nil {
			t.Fatal(err)
		}
		runner := &fakeRunner{calls: map[string]int{}}
		ctx, cancel := context.WithCancel(bg)
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Store: s, Runner: runner, Pings: p, HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		}()
		waitFor(t, "ping results", func() bool { rows, _ := s.Recent(bg, "router", 10); return len(rows) >= 2 })
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
