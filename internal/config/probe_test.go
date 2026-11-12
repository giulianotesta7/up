package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

func pingCheck() config.Check {
	return config.Check{Name: "router", Kind: config.CheckPing, Host: "192.168.1.1", Interval: 30 * time.Second, Timeout: 2 * time.Second}
}

func dnsCheck() config.Check {
	return config.Check{Name: "nas", Kind: config.CheckDNS, Domain: "nas.home.arpa", Record: "A", Interval: 30 * time.Second, Timeout: 2 * time.Second}
}

func TestValidatePingAndDNS(t *testing.T) {
	tests := []struct {
		name   string
		base   func() config.Check
		change func(*config.Check)
		want   string
	}{
		{"ping valid", pingCheck, func(*config.Check) {}, ""},
		{"ping host name", pingCheck, func(c *config.Check) { c.Host = "nas.home.arpa" }, ""},
		{"ping ipv6", pingCheck, func(c *config.Check) { c.Host = "fd00::1" }, ""},
		{"ping shared settings", pingCheck, func(c *config.Check) { c.Channel, c.AlertAfter, c.RecoverAfter = "", 3, 2 }, ""},
		{"ping host missing", pingCheck, func(c *config.Check) { c.Host = "" }, "host is required"},
		{"ping host brackets", pingCheck, func(c *config.Check) { c.Host = "[::1]" }, `host "[::1]" must be a host name or IP address, without scheme, brackets or port`},
		{"ping host port", pingCheck, func(c *config.Check) { c.Host = "router:22" }, `host "router:22" must be a host name or IP address`},
		{"ping name", pingCheck, func(c *config.Check) { c.Name = "" }, "name is required"},
		{"ping interval", pingCheck, func(c *config.Check) { c.Interval = 0 }, "interval must be greater than 0"},
		{"ping timeout", pingCheck, func(c *config.Check) { c.Timeout = 0 }, "timeout must be greater than 0"},
		{"ping timeout above interval", pingCheck, func(c *config.Check) { c.Timeout = time.Minute }, "must not exceed interval"},
		{"ping port", pingCheck, func(c *config.Check) { c.Port = 22 }, "port is only for tcp checks"},
		{"ping url", pingCheck, func(c *config.Check) { c.URL = "https://example.com" }, "url is only for http checks"},
		{"ping status", pingCheck, func(c *config.Check) { c.ExpectedStatus = 200 }, "expected status is only for http checks"},
		{"ping method", pingCheck, func(c *config.Check) { c.Method = "POST" }, "method, headers, body and expected text are only for http checks"},
		{"ping body", pingCheck, func(c *config.Check) { c.Body = sealed(1) }, "method, headers, body and expected text are only for http checks"},
		{"ping conditions", pingCheck, func(c *config.Check) {
			c.Conditions = []config.Condition{{Kind: config.ConditionResponseTime, Within: time.Second}}
		}, "conditions are only for http checks"},
		{"ping tls", pingCheck, func(c *config.Check) { c.TLSOff = true }, "tls is only for http checks"},
		{"ping period", pingCheck, func(c *config.Check) { c.Period = time.Hour }, "period and grace are only for heartbeat checks"},
		{"ping dns fields", pingCheck, func(c *config.Check) { c.Record = "A" }, "domain, record, resolver and expected value are only for dns checks"},

		{"dns valid", dnsCheck, func(*config.Check) {}, ""},
		{"dns trailing dot", dnsCheck, func(c *config.Check) { c.Domain = "example.com." }, ""},
		{"dns underscore label", dnsCheck, func(c *config.Check) { c.Domain, c.Record = "_dmarc.example.com", "TXT" }, ""},
		{"dns every record", dnsCheck, func(c *config.Check) { c.Record = "MX" }, ""},
		{"dns resolver ipv4", dnsCheck, func(c *config.Check) { c.Resolver = "192.0.2.53" }, ""},
		{"dns resolver ipv4 port", dnsCheck, func(c *config.Check) { c.Resolver = "192.0.2.53:5353" }, ""},
		{"dns resolver ipv6 brackets", dnsCheck, func(c *config.Check) { c.Resolver = "[fd00::53]:53" }, ""},
		{"dns resolver ipv6 brackets no port", dnsCheck, func(c *config.Check) { c.Resolver = "[fd00::53]" }, ""},
		{"dns resolver name", dnsCheck, func(c *config.Check) { c.Resolver = "dns.home.arpa:53" }, ""},
		{"dns expected a", dnsCheck, func(c *config.Check) { c.Expected = "203.0.113.10" }, ""},
		{"dns expected aaaa", dnsCheck, func(c *config.Check) { c.Record, c.Expected = "AAAA", "2001:db8::10" }, ""},
		{"dns expected cname", dnsCheck, func(c *config.Check) { c.Record, c.Expected = "CNAME", "edge.example.net." }, ""},
		{"dns expected txt", dnsCheck, func(c *config.Check) { c.Record, c.Expected = "TXT", "v=spf1 include:_spf.example.com ~all" }, ""},
		{"dns domain missing", dnsCheck, func(c *config.Check) { c.Domain = "" }, "domain is required"},
		{"dns domain url", dnsCheck, func(c *config.Check) { c.Domain = "https://example.com" }, `domain "https://example.com" must be a domain name like example.com`},
		{"dns domain space", dnsCheck, func(c *config.Check) { c.Domain = "my host" }, `domain "my host" must be a domain name like example.com`},
		{"dns domain empty label", dnsCheck, func(c *config.Check) { c.Domain = "a..b" }, `domain "a..b" must be a domain name like example.com`},
		{"dns domain long label", dnsCheck, func(c *config.Check) { c.Domain = strings.Repeat("a", 64) + ".com" }, "must be a domain name like example.com"},
		{"dns record missing", dnsCheck, func(c *config.Check) { c.Record = "" }, "record is required"},
		{"dns record unknown", dnsCheck, func(c *config.Check) { c.Record = "SRV" }, `record "SRV" must be one of A, AAAA, CNAME, MX, TXT`},
		{"dns record lowercase", dnsCheck, func(c *config.Check) { c.Record = "a" }, `record "a" must be one of A, AAAA, CNAME, MX, TXT`},
		{"dns resolver bare ipv6", dnsCheck, func(c *config.Check) { c.Resolver = "fd00::53" }, `resolver "fd00::53" must put an IPv6 address in brackets, like [fd00::53]:53`},
		{"dns resolver bad port", dnsCheck, func(c *config.Check) { c.Resolver = "192.0.2.53:0" }, `resolver "192.0.2.53:0" must be host or host:port with a port between 1 and 65535`},
		{"dns resolver scheme", dnsCheck, func(c *config.Check) { c.Resolver = "udp://192.0.2.53" }, `resolver "udp://192.0.2.53" must be host or host:port`},
		{"dns resolver bracketed ipv4", dnsCheck, func(c *config.Check) { c.Resolver = "[192.0.2.53]:53" }, `resolver "[192.0.2.53]:53" must be host or host:port`},
		{"dns expected a not ipv4", dnsCheck, func(c *config.Check) { c.Expected = "2001:db8::1" }, `expected value "2001:db8::1" must be an IPv4 address for A records`},
		{"dns expected aaaa not ipv6", dnsCheck, func(c *config.Check) { c.Record, c.Expected = "AAAA", "203.0.113.10" }, `expected value "203.0.113.10" must be an IPv6 address for AAAA records`},
		{"dns expected mx not a name", dnsCheck, func(c *config.Check) { c.Record, c.Expected = "MX", "10 mail.example.com" }, `expected value "10 mail.example.com" must be a host name for MX records`},
		{"dns expected too long", dnsCheck, func(c *config.Check) { c.Record, c.Expected = "TXT", strings.Repeat("a", config.MaxDNSExpected+1) }, "expected value must be at most 1024 bytes"},
		{"dns interval", dnsCheck, func(c *config.Check) { c.Interval = 0 }, "interval must be greater than 0"},
		{"dns host", dnsCheck, func(c *config.Check) { c.Host = "x" }, "host and port are not used by dns checks"},
		{"dns url", dnsCheck, func(c *config.Check) { c.URL = "https://example.com" }, "url is only for http checks"},
		{"dns status", dnsCheck, func(c *config.Check) { c.ExpectedStatus = 200 }, "expected status is only for http checks"},
		{"dns expect text", dnsCheck, func(c *config.Check) { c.ExpectText = "ok" }, "method, headers, body and expected text are only for http checks"},
		{"dns tls", dnsCheck, func(c *config.Check) { c.TLSOff = true }, "tls is only for http checks"},
		{"dns grace", dnsCheck, func(c *config.Check) { c.Grace = time.Minute }, "period and grace are only for heartbeat checks"},

		{"http with dns fields", httpCheck, func(c *config.Check) { c.Domain = "example.com" }, "domain, record, resolver and expected value are only for dns checks"},
		{"tcp with dns fields", tcpCheck, func(c *config.Check) { c.Expected = "x" }, "domain, record, resolver and expected value are only for dns checks"},
		{"heartbeat with dns fields", heartbeatCheck, func(c *config.Check) { c.Resolver = "192.0.2.53" }, "domain, record, resolver and expected value are only for dns checks"},
		{"unknown kind lists all", httpCheck, func(c *config.Check) { c.Kind = "icmp" }, `kind "icmp" must be http, tcp, heartbeat, ping or dns`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := tt.base()
			tt.change(&c)
			err := config.ValidateCheck(c)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateCheck() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDNSRecordsAndResolverAddress(t *testing.T) {
	if got := strings.Join(config.DNSRecords, " "); got != "A AAAA CNAME MX TXT" {
		t.Fatalf("DNSRecords = %q", got)
	}
	for in, want := range map[string]string{
		"":                 "",
		"192.0.2.53":       "192.0.2.53:53",
		"192.0.2.53:5353":  "192.0.2.53:5353",
		"[fd00::53]":       "[fd00::53]:53",
		"[fd00::53]:5353":  "[fd00::53]:5353",
		"dns.home.arpa":    "dns.home.arpa:53",
		"dns.home.arpa:54": "dns.home.arpa:54",
	} {
		if got := config.ResolverAddress(in); got != want {
			t.Errorf("ResolverAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPingAndDNSDefinitionIdentity(t *testing.T) {
	d := dnsCheck()
	d.Resolver, d.Expected = "192.0.2.53", "203.0.113.10"
	for _, change := range []func(*config.Check){
		func(c *config.Check) { c.Domain = "other.home.arpa" },
		func(c *config.Check) { c.Record = "AAAA" },
		func(c *config.Check) { c.Resolver = "" },
		func(c *config.Check) { c.Expected = "203.0.113.11" },
	} {
		other := d
		change(&other)
		if d.SameDefinition(other) {
			t.Fatalf("%+v and %+v are the same definition", d, other)
		}
	}
	if !d.SameDefinition(d) {
		t.Fatal("a definition differs from itself")
	}
	if got := d.Target(); got != "A nas.home.arpa" {
		t.Fatalf("dns Target() = %q", got)
	}
	if got := pingCheck().Target(); got != "192.168.1.1" {
		t.Fatalf("ping Target() = %q", got)
	}
}

func TestPingPermission(t *testing.T) {
	if config.PingUnknown != "" || config.PingAllowed != "allowed" || config.PingDenied != "denied" {
		t.Fatalf("permission wire values changed")
	}
	if config.PingDeniedReason != "cannot send pings" {
		t.Fatalf("PingDeniedReason = %q", config.PingDeniedReason)
	}
	ping, http := pingCheck(), httpCheck()
	if !config.PingDenied.Holds(ping) || config.PingAllowed.Holds(ping) || config.PingUnknown.Holds(ping) || config.PingDenied.Holds(http) {
		t.Fatal("Holds must be true only for ping checks on a daemon that cannot ping")
	}
}
