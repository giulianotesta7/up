package client

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
)

// Ping and DNS monitors are created, edited, duplicated, muted and given a
// maintenance window over --remote, and the snapshot carries their answer and
// the daemon's ping permission.
func TestPingAndDNSThroughTheClient(t *testing.T) {
	c, s, _ := realServer(t)
	ctx := context.Background()
	ping := config.Check{Name: "router", Kind: config.CheckPing, Host: "fd00::1", Interval: 30 * time.Second, Timeout: 2 * time.Second, AlertAfter: 3}
	dns := config.Check{Name: "nas", Kind: config.CheckDNS, Domain: "nas.home.arpa", Record: "AAAA", Resolver: "[fd00::53]:5353", Expected: "fd00::10", Interval: time.Minute, Timeout: 2 * time.Second}
	for _, v := range []config.Check{ping, dns} {
		if err := c.AddCheck(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	dns.Record, dns.Expected, dns.Resolver = "TXT", "v=spf1 -all", ""
	if err := c.UpdateCheck(ctx, dns); err != nil {
		t.Fatal(err)
	}
	copied := dns
	copied.Name = "nas copy"
	if err := c.AddCheckFrom(ctx, "nas", copied); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Checks(ctx)
	if err != nil || len(stored) != 3 || !reflect.DeepEqual(stored[0], ping) || !reflect.DeepEqual(stored[1], dns) || !reflect.DeepEqual(stored[2], copied) {
		t.Fatalf("stored %+v, %v", stored, err)
	}
	until := time.Now().Add(time.Hour).Truncate(time.Minute)
	if err := c.SetMute(ctx, "router", until, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "nas", Weekdays: []time.Weekday{time.Sunday}, StartMinute: 120, Duration: time.Hour, Zone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	answered := check.Result{CheckName: "nas", At: time.Now().Add(-time.Minute).Truncate(time.Millisecond).UTC(), Status: check.StatusUp, Latency: 3 * time.Millisecond, Answer: "v=spf1 -all", Answers: 1}
	if err := s.Record(ctx, answered); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPingPermission(ctx, config.PingAllowed); err != nil {
		t.Fatal(err)
	}
	snap, err := c.LoadSnapshot(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Pings != config.PingAllowed {
		t.Fatalf("snapshot pings = %q, want allowed", snap.Pings)
	}
	if !reflect.DeepEqual(snap.Services[0].Check, ping) || !snap.Services[0].HasMute || !reflect.DeepEqual(snap.Services[1].Check, dns) || len(snap.Services[1].Maintenance) != 1 {
		t.Fatalf("snapshot services %+v", snap.Services)
	}
	if got := snap.Services[1].Latest; got.Answer != answered.Answer || got.Answers != 1 || got.Status != check.StatusUp {
		t.Fatalf("latest = %+v, want the answer %q", got, answered.Answer)
	}
}
