package client

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

func TestDefaultsRoundTripThroughTheRealAPI(t *testing.T) {
	c, s, _ := realServer(t)
	ctx := context.Background()
	snap, err := c.LoadSnapshot(ctx, time.Now())
	if err != nil || snap.Defaults != config.BuiltinDefaults() {
		t.Fatalf("snapshot defaults = %+v, %v, want built-in", snap.Defaults, err)
	}
	want := config.Defaults{Zone: "Europe/Madrid", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204, AlertAfter: 3, RecoverAfter: 2}
	if err := c.SetDefaults(ctx, want); err != nil {
		t.Fatalf("SetDefaults() = %v", err)
	}
	if got, _ := s.Defaults(ctx); got != want {
		t.Fatalf("stored = %+v, want %+v", got, want)
	}
	if snap, err = c.LoadSnapshot(ctx, time.Now()); err != nil || snap.Defaults != want {
		t.Fatalf("snapshot defaults = %+v, %v, want %+v", snap.Defaults, err, want)
	}

	// The server's validator message reaches the caller and nothing changes.
	bad := want
	bad.Timeout, bad.Interval = 10*time.Second, 5*time.Second
	if err := c.SetDefaults(ctx, bad); err == nil || err.Error() != "store: set defaults: timeout (10s) must not exceed interval (5s)" {
		t.Fatalf("SetDefaults(invalid) = %v", err)
	}
	bad = want
	bad.Channel = "ghost"
	if err := c.SetDefaults(ctx, bad); err == nil || !strings.Contains(err.Error(), `channel "ghost" does not exist`) {
		t.Fatalf("SetDefaults(missing channel) = %v", err)
	}
	if got, _ := s.Defaults(ctx); got != want {
		t.Fatalf("rejected writes changed defaults: %+v", got)
	}
}
