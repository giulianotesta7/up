package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

func ping(name string) config.Check {
	return config.Check{Name: name, Kind: config.CheckPing, Host: "192.168.1.1", Interval: 30 * time.Second, Timeout: 2 * time.Second, AlertAfter: 2}
}

func dns(name string) config.Check {
	return config.Check{Name: name, Kind: config.CheckDNS, Domain: "nas.home.arpa", Record: "A", Resolver: "[fd00::53]:53", Expected: "203.0.113.10", Interval: time.Minute, Timeout: 2 * time.Second}
}

func TestPingAndDNSChecksRoundTrip(t *testing.T) {
	s, _ := seeded(t)
	for _, c := range []config.Check{ping("router"), dns("nas")} {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
		if got := stored(t, s, c.Name); !got.SameDefinition(c) || got.Domain != c.Domain || got.Resolver != c.Resolver {
			t.Fatalf("round trip lost a field:\n got %+v\nwant %+v", got, c)
		}
	}
	// Edit changes every DNS field; a rejected edit changes nothing.
	edited := dns("nas")
	edited.Domain, edited.Record, edited.Resolver, edited.Expected = "example.com", "MX", "", "mail.example.com"
	if err := s.UpdateCheck(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, s, "nas"); !got.SameDefinition(edited) {
		t.Fatalf("edited = %+v, want %+v", got, edited)
	}
	bad := edited
	bad.Record = "SRV"
	if err := s.UpdateCheck(ctx, bad); !errors.Is(err, store.ErrValidation) || !stored(t, s, "nas").SameDefinition(edited) {
		t.Fatalf("invalid edit: %v", err)
	}
	// Switching a DNS check to ping leaves no DNS field behind.
	if err := s.UpdateCheck(ctx, ping("nas")); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, s, "nas"); got.Kind != config.CheckPing || got.Domain != "" || got.Record != "" || got.Resolver != "" || got.Expected != "" {
		t.Fatalf("switch to ping: %+v", got)
	}
}

func TestRecordDefinedComparesDNSFields(t *testing.T) {
	s, _ := seeded(t)
	c := dns("nas")
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	r := check.Result{CheckName: "nas", At: at, Status: check.StatusUp, Latency: 3 * time.Millisecond, Answer: "203.0.113.10", Answers: 2}
	if ok, err := s.RecordDefined(ctx, c, r); err != nil || !ok {
		t.Fatalf("RecordDefined(current) = %v, %v", ok, err)
	}
	for _, change := range []func(*config.Check){
		func(c *config.Check) { c.Domain = "other.home.arpa" },
		func(c *config.Check) { c.Record = "AAAA" },
		func(c *config.Check) { c.Resolver = "" },
		func(c *config.Check) { c.Expected = "" },
	} {
		old := c
		change(&old)
		if ok, err := s.RecordDefined(ctx, old, r); err != nil || ok {
			t.Fatalf("RecordDefined(%+v) = %v, %v, want a stale result dropped", old, ok, err)
		}
	}
	got, ok, err := s.Latest(ctx, "nas")
	if err != nil || !ok || got != r {
		t.Fatalf("Latest() = %+v, %v, %v, want %+v with its answer", got, ok, err, r)
	}
}

func TestRecordKeepsTheDNSAnswer(t *testing.T) {
	s, _ := seeded(t)
	r := check.Result{CheckName: "web", At: at, Status: check.StatusDown, Error: "A = 203.0.113.9, want 203.0.113.10", Answer: "203.0.113.9", Answers: 1}
	if err := s.Record(ctx, r); err != nil {
		t.Fatal(err)
	}
	recent, err := s.Recent(ctx, "web", 1)
	if err != nil || len(recent) != 1 || recent[0] != r {
		t.Fatalf("Recent() = %+v, %v, want %+v", recent, err, r)
	}
}

func TestAddCheckFromNeverGivesAPingOrDNSCheckHTTPValues(t *testing.T) {
	s, _ := seeded(t)
	if err := s.AddCheck(ctx, advanced("adv")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []config.Check{ping("router"), dns("nas")} {
		if err := s.AddCheckFrom(ctx, "adv", c); err != nil {
			t.Fatal(err)
		}
		if got := stored(t, s, c.Name); !got.Body.Empty() || len(got.Headers) != 0 || !got.SameDefinition(c) {
			t.Fatalf("%s copy = %+v", c.Kind, got)
		}
	}
}

func TestPingPermissionIsUnknownUntilTheDaemonReportsIt(t *testing.T) {
	s, path := seeded(t)
	if got, err := s.PingPermission(ctx); err != nil || got != config.PingUnknown {
		t.Fatalf("PingPermission() before any report = %q, %v", got, err)
	}
	for _, p := range []config.PingPermission{config.PingDenied, config.PingAllowed} {
		if err := s.SetPingPermission(ctx, p); err != nil {
			t.Fatal(err)
		}
		if got, err := s.PingPermission(ctx); err != nil || got != p {
			t.Fatalf("PingPermission() = %q, %v, want %q", got, err, p)
		}
	}
	if err := s.SetPingPermission(ctx, "maybe"); err == nil {
		t.Fatal("an unknown permission was stored")
	}
	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if got, err := ro.PingPermission(ctx); err != nil || got != config.PingAllowed {
		t.Fatalf("read-only PingPermission() = %q, %v", got, err)
	}
}
