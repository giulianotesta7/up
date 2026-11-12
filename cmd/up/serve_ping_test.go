package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

// serve detects at start whether it can send pings and records it for the
// snapshot; without permission it says so once and keeps running.
func TestServeRecordsWhetherItCanSendPings(t *testing.T) {
	for _, p := range []config.PingPermission{config.PingDenied, config.PingAllowed} {
		t.Run(string(p), func(t *testing.T) {
			isolateDefaultDatabase(t)
			previous := detectPings
			detectPings = func() config.PingPermission { return p }
			t.Cleanup(func() { detectPings = previous })
			path := filepath.Join(t.TempDir(), "up.db")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type exit struct {
				code   int
				errOut string
			}
			done := make(chan exit, 1)
			go func() { code, _, errOut := runCmd(ctx, "serve", "--db", path); done <- exit{code, errOut} }()
			deadline := time.Now().Add(5 * time.Second)
			for got := config.PingUnknown; got != p; time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("serve did not record ping permission %q within 5s", p)
				}
				if s, err := store.OpenReadOnly(path); err == nil {
					got, _ = s.PingPermission(context.Background())
					s.Close()
				}
			}
			cancel()
			got := <-done
			if got.code != 0 {
				t.Fatalf("%d %s", got.code, got.errOut)
			}
			warned := strings.Contains(got.errOut, "cannot send pings: ping monitors stay pending")
			if warned != (p == config.PingDenied) {
				t.Fatalf("stderr %q, want the warning only without permission", got.errOut)
			}
		})
	}
}

func TestServeHelpDescribesPing(t *testing.T) {
	isolateDefaultDatabase(t)
	code, out, _ := runCmd(context.Background(), "serve", "--help")
	if code != 0 || !strings.Contains(out, "Ping monitors need ICMP") || !strings.Contains(out, "net.ipv4.ping_group_range") {
		t.Fatalf("help = %d %q", code, out)
	}
}
