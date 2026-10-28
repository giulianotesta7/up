package monitor

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

func TestMuteReconcilesAcrossRestart(t *testing.T) {
	s := openStore(t)
	alertingCheck(t, s, 1, 1)
	a := &recordingAlerter{}
	ctx := context.Background()
	D, U := check.StatusDown, check.StatusUp
	if err := s.SetMute(ctx, "api", time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	feed(t, s, a, 0, D, U, D)
	if got := a.snapshot(); len(got) != 0 {
		t.Fatalf("muted alerts: %v", got)
	}
	if err := s.Unmute(ctx, "api", time.Now()); err != nil {
		t.Fatal(err)
	}
	feed(t, s, a, 3, D)
	if got := a.snapshot(); !reflect.DeepEqual(got, []check.Status{D}) {
		t.Fatalf("reconciled: %v", got)
	}
	if _, ok, err := s.Mute(ctx, "api"); err != nil || ok {
		t.Fatalf("expired mute: %v %v", ok, err)
	}
	if err := s.SetMute(ctx, "api", time.Now().Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	feed(t, s, a, 4, U)
	if got := a.snapshot(); len(got) != 1 {
		t.Fatalf("muted recovery: %v", got)
	}
	if err := s.Unmute(ctx, "api", time.Now()); err != nil {
		t.Fatal(err)
	}
	feed(t, s, a, 5, U)
	if got := a.snapshot(); !reflect.DeepEqual(got, []check.Status{D, U}) {
		t.Fatalf("recovery: %v", got)
	}
}

func TestMuteExpiresOnItsOwn(t *testing.T) {
	s := openStore(t)
	alertingCheck(t, s, 1, 1)
	a := &recordingAlerter{}
	until := time.Now().Add(150 * time.Millisecond)
	if err := s.SetMute(context.Background(), "api", until, false); err != nil {
		t.Fatal(err)
	}
	D := check.StatusDown
	feed(t, s, a, 0, D, D)
	if got := a.snapshot(); len(got) != 0 {
		t.Fatalf("alerts while muted: %v", got)
	}
	waitFor(t, "mute to end", func() bool { return time.Now().After(until) })
	feed(t, s, a, 2, D, D)
	if got := a.snapshot(); !reflect.DeepEqual(got, []check.Status{D}) {
		t.Fatalf("alerts after expiry: %v, want exactly one down alert", got)
	}
}

func TestMuteDoesNotAffectOtherMonitors(t *testing.T) {
	s := openStore(t)
	alertingCheck(t, s, 1, 1)
	other := config.Check{Name: "web", URL: "http://example.com", Interval: 10 * time.Millisecond, Timeout: time.Millisecond, ExpectedStatus: 200, Channel: "mail"}
	if err := s.AddCheck(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMute(context.Background(), "api", time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	a := &recordingAlerter{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Store: s, Runner: downRunner{}, Alerter: a, HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	waitFor(t, "web alert", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.names) > 0 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.names) != 1 || a.names[0] != "web" {
		t.Fatalf("alerted monitors: %v, want only web", a.names)
	}
}
