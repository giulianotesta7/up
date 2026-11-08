package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

var (
	pingFixture = config.Check{Name: "router", Kind: config.CheckPing, Host: "192.168.1.1", Interval: 30 * time.Second, Timeout: 2 * time.Second}
	dnsFixture  = config.Check{Name: "api-dns", Kind: config.CheckDNS, Domain: "api.example.com", Record: "A", Expected: "203.0.113.10",
		Interval: time.Minute, Timeout: 5 * time.Second, AlertAfter: 2}
)

// probeModel is a model over a real store with checks, the daemon's ping
// permission (unknown when empty) and a terminal of width x height.
func probeModel(t *testing.T, perm config.PingPermission, width, height int, checks ...config.Check) (*store.Store, Model) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if perm != config.PingUnknown {
		if err := s.SetPingPermission(ctx, perm); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range checks {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	return s, sizedModel(t, New(s, func() time.Time { return now }), width, height)
}

// selectNamed selects the monitor called name in the list.
func selectNamed(t *testing.T, m Model, name string) Model {
	t.Helper()
	for i, svc := range m.snap.Services {
		if svc.Check.Name == name {
			m.selected = i
			return m
		}
	}
	t.Fatalf("no monitor %q", name)
	return m
}

// openKind opens a new monitor form and presses → on Type n times.
func openKind(t *testing.T, m Model, n int) Model {
	t.Helper()
	m, _ = press(t, m, keyN, keyShiftTab)
	for range n {
		m, _ = press(t, m, keyRight)
	}
	return m
}

const (
	pingCopyLine   = "One echo request per check; down if no reply within the timeout."
	dnsCopyLine    = "Down when it does not resolve or no answer equals Expected."
	pingDeniedText = "! up serve cannot send pings. README › Ping says how to allow it."
)

func TestTypeCyclesThroughFiveKinds(t *testing.T) {
	_, m := probeModel(t, "", 120, 40)
	m, _ = press(t, m, keyN, keyShiftTab)
	for _, want := range []string{"TCP", "Heartbeat", "Ping", "DNS", "HTTP"} {
		m, _ = press(t, m, keyRight)
		requireText(t, m, "Type: ‹ "+want+" › ")
	}
	m, _ = press(t, m, keyArrowLeft)
	requireText(t, m, "Type: ‹ DNS › ")
	m, _ = press(t, m, keyArrowLeft)
	requireText(t, m, "Type: ‹ Ping › ")
}

func TestPingFormShowsOnlyPingRows(t *testing.T) {
	_, m := probeModel(t, "", 120, 40)
	m = openKind(t, m, 3)
	requireText(t, m, "Type: ‹ Ping › ", "Name: ", "Host: ", "Interval: 30s", "Timeout: 5s", pingCopyLine,
		"Channel: ‹ none ›", "Alert after: ‹ 1 › failures in a row", "Recover after: ‹ 1 › successes in a row")
	requireAbsent(t, m, "HTTP/HTTPS URL", "Port", "Period", "Grace", "Expected status", "TLS expiry", "Advanced",
		"Domain", "Record", "Resolver", "Expected:", "cannot send pings")
	// The order is Type, Name, Host, Interval, Timeout.
	out := view(m)
	if !(strings.Index(out, "Name:") < strings.Index(out, "Host:") && strings.Index(out, "Host:") < strings.Index(out, "Interval:") &&
		strings.Index(out, "Interval:") < strings.Index(out, "Timeout:")) {
		t.Fatalf("ping rows out of order:\n%s", out)
	}
}

func TestDNSFormShowsOnlyDNSRowsAndCyclesRecords(t *testing.T) {
	_, m := probeModel(t, "", 120, 40)
	m = openKind(t, m, 4)
	requireText(t, m, "Type: ‹ DNS › ", "Name: ", "Domain: ", "Record: ‹ A ›", "Resolver: system resolver", "Expected: any answer",
		"Interval: 30s", "Timeout: 5s", dnsCopyLine, "Channel: ‹ none ›")
	requireAbsent(t, m, "HTTP/HTTPS URL", "Host", "Port", "Period", "Expected status", "TLS expiry", "Advanced", pingCopyLine)
	m, _ = press(t, m, keyTab, keyTab, keyTab) // Name, Domain, Record
	requireText(t, m, "> Record: ‹ A ›", "←/→ choose")
	for _, want := range []string{"AAAA", "CNAME", "MX", "TXT", "A"} {
		m, _ = press(t, m, keyRight)
		requireText(t, m, "Record: ‹ "+want+" ›")
	}
	m, _ = press(t, m, keyTab) // Resolver: the hint stays while it is empty
	requireText(t, m, "> Resolver: │system resolver")
	m = typeText(t, m, "192.0.2.53")
	requireText(t, m, "Resolver: 192.0.2.53│")
	requireAbsent(t, m, "system resolver")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "203.0.113.10")
	requireText(t, m, "Expected: 203.0.113.10│")
	requireAbsent(t, m, "any answer")
}

func TestFormBoxStaysPutAcrossFiveTypes(t *testing.T) {
	for _, perm := range []config.PingPermission{config.PingUnknown, config.PingDenied} {
		for _, size := range [][2]int{{120, 40}, {80, 24}} {
			_, m := probeModel(t, perm, size[0], size[1])
			m, _ = press(t, m, keyN, keyShiftTab)
			want := boxGeometry(t, m, "New monitor")
			for i := range 5 {
				m, _ = press(t, m, keyRight)
				if got := boxGeometry(t, m, "New monitor"); got != want {
					t.Fatalf("%q %v: switch %d: box %v, was %v:\n%s", perm, size, i, got, want, view(m))
				}
				requireWidth(t, m, size[0])
			}
			m, _ = press(t, m, keyArrowLeft) // DNS
			for i := range 11 {
				m, _ = press(t, m, keyTab)
				if got := boxGeometry(t, m, "New monitor"); got != want {
					t.Fatalf("%q %v: tab %d: box %v, was %v:\n%s", perm, size, i, got, want, view(m))
				}
			}
		}
	}
}

func TestPingFormSavesWithDefaults(t *testing.T) {
	ctx := context.Background()
	s, m := probeModel(t, config.PingAllowed, 120, 40)
	d := config.BuiltinDefaults()
	d.Interval, d.Timeout, d.AlertAfter = time.Minute, 3*time.Second, 2
	if err := s.SetDefaults(ctx, d); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	m = openKind(t, m, 3)
	requireText(t, m, "Interval: 1m", "Timeout: 3s", "Alert after: ‹ 2 ›")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "router")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "fd00::1")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	got, ok := storedByName(t, s, "router")
	if !ok || got.Kind != config.CheckPing || got.Host != "fd00::1" || got.Interval != time.Minute || got.Timeout != 3*time.Second ||
		got.AlertAfter != 2 || got.Port != 0 || got.URL != "" || got.ExpectedStatus != 0 || got.TLSOff {
		t.Fatalf("saved %+v, %v", got, ok)
	}
}

func TestDNSFormSavesEveryField(t *testing.T) {
	s, m := probeModel(t, "", 120, 40)
	m = openKind(t, m, 4)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "mail")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "example.com")
	m, _ = press(t, m, keyTab, keyRight, keyRight, keyRight) // MX
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "[fd00::53]:53")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "mx1.example.com")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	got, ok := storedByName(t, s, "mail")
	if !ok || got.Kind != config.CheckDNS || got.Domain != "example.com" || got.Record != "MX" || got.Resolver != "[fd00::53]:53" ||
		got.Expected != "mx1.example.com" || got.Host != "" || got.URL != "" || got.ExpectedStatus != 0 || got.TLSOff ||
		got.Interval != 30*time.Second || got.Timeout != 5*time.Second {
		t.Fatalf("saved %+v, %v", got, ok)
	}
}

func TestDNSFormRejectsBadValuesAndKeepsStore(t *testing.T) {
	for _, tc := range []struct{ resolver, expected, want string }{
		{"fd00::53", "", `resolver "fd00::53" must put an IPv6 address in brackets, like [fd00::53]:53`},
		{"", "2001:db8::1", `expected value "2001:db8::1" must be an IPv4 address for A records`},
	} {
		s, m := probeModel(t, "", 120, 40)
		m = openKind(t, m, 4)
		m, _ = press(t, m, keyTab)
		m = typeText(t, m, "nas")
		m, _ = press(t, m, keyTab)
		m = typeText(t, m, "nas.home.arpa")
		m, _ = press(t, m, keyTab, keyTab)
		m = typeText(t, m, tc.resolver)
		m, _ = press(t, m, keyTab)
		m = typeText(t, m, tc.expected)
		m, cmd := press(t, m, keyEnter)
		if cmd != nil {
			t.Fatalf("%+v saved", tc)
		}
		requireText(t, m, "Cannot save monitor: "+tc.want)
		if got, _ := s.Checks(context.Background()); len(got) != 0 {
			t.Fatalf("stored %+v", got)
		}
	}
}

func TestSwitchingAnHTTPMonitorWithTLSOffToPingOrDNSSaves(t *testing.T) {
	for _, kind := range []string{"Ping", "DNS"} {
		c := testChecks[0]
		c.TLSOff = true
		s, m := probeModel(t, "", 120, 40, c)
		m, _ = press(t, m, keyE, keyShiftTab, keyArrowLeft)
		if kind == "Ping" {
			m, _ = press(t, m, keyArrowLeft)
		}
		requireText(t, m, "Type: ‹ "+kind+" › ")
		m, _ = press(t, m, keyTab)
		m = typeText(t, m, "api.example.com")
		m, cmd := press(t, m, keyEnter)
		if cmd == nil {
			t.Fatalf("%s: not saved:\n%s", kind, view(m))
		}
		finishCommand(t, m, cmd)
		got, _ := storedByName(t, s, "api")
		if got.TLSOff || got.URL != "" || got.ExpectedStatus != 0 || (got.Kind != config.CheckPing && got.Kind != config.CheckDNS) {
			t.Fatalf("%s: saved %+v", kind, got)
		}
	}
}

func TestPingDeniedFormLine(t *testing.T) {
	s, m := probeModel(t, config.PingDenied, 120, 40)
	m = openKind(t, m, 3)
	requireText(t, m, pingDeniedText)
	m, _ = press(t, m, keyRight) // DNS needs no permission
	requireAbsent(t, m, "cannot send pings")
	m, _ = press(t, m, keyArrowLeft, keyTab)
	m = typeText(t, m, "router")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "192.168.1.1")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	if _, ok := storedByName(t, s, "router"); !ok {
		t.Fatal("saving a ping monitor without permission is still allowed")
	}

	for _, perm := range []config.PingPermission{config.PingUnknown, config.PingAllowed} {
		_, m := probeModel(t, perm, 120, 40)
		m = openKind(t, m, 3)
		requireText(t, m, "Type: ‹ Ping › ")
		requireAbsent(t, m, "cannot send pings")
	}
}

// probeHistory records an old up result for router and a DNS answer for
// api-dns.
func probeHistory(t *testing.T, s *store.Store, answers int) {
	t.Helper()
	ctx := context.Background()
	for _, r := range []check.Result{
		{CheckName: "router", At: at("03:44:56"), Status: check.StatusUp, Latency: time.Millisecond},
		{CheckName: "api-dns", At: at("03:44:20"), Status: check.StatusUp, Latency: 18 * time.Millisecond, Answer: "203.0.113.10", Answers: answers},
	} {
		if err := s.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPingAndDNSListRows(t *testing.T) {
	s, m := probeModel(t, config.PingAllowed, 100, 40, pingFixture, dnsFixture, tcpCheckFixture)
	probeHistory(t, s, 2)
	m = reload(t, m)
	requireRow(t, heartbeatRow(t, m, "router"), "router   ping  UP", "1ms", "4s ago")
	requireRow(t, heartbeatRow(t, m, "api-dns"), "api-dns  dns   UP", "18ms", "40s ago", "A 203.0.113.10 +1")
	requireRow(t, heartbeatRow(t, m, "database"), "database tcp   PENDING")

	s, m = probeModel(t, config.PingAllowed, 100, 40, dnsFixture)
	probeHistory(t, s, 1)
	m = reload(t, m)
	row := heartbeatRow(t, m, "api-dns")
	requireRow(t, row, "api-dns dns  UP", "A 203.0.113.10")
	if strings.Contains(row, "+") {
		t.Fatalf("one answer shows a count: %q", row)
	}
}

func TestPingDeniedListAndDetailShowPending(t *testing.T) {
	s, m := probeModel(t, config.PingDenied, 100, 40, pingFixture, dnsFixture)
	probeHistory(t, s, 1)
	m = reload(t, m)
	row := heartbeatRow(t, m, "router")
	requireRow(t, row, "PENDING", "cannot send pings")
	for _, unwanted := range []string{"UP", "1ms", "4s ago", "▁"} {
		if strings.Contains(row, unwanted) {
			t.Fatalf("held row shows %q: %q", unwanted, row)
		}
	}
	requireRow(t, heartbeatRow(t, m, "api-dns"), "UP", "18ms")
	m = selectNamed(t, m, "router")
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "router  ping 192.168.1.1", "Status       PENDING", "Last error   cannot send pings · README › Ping")
	requireAbsent(t, m, "last check", "UP")

	s, m = probeModel(t, config.PingUnknown, 100, 40, pingFixture, dnsFixture)
	probeHistory(t, s, 1)
	m = reload(t, m)
	requireRow(t, heartbeatRow(t, m, "router"), "UP", "1ms")
	requireAbsent(t, m, "cannot send pings")
}

func TestPingAndDNSDetailRows(t *testing.T) {
	s, m := probeModel(t, config.PingAllowed, 100, 50, pingFixture, dnsFixture)
	probeHistory(t, s, 2)
	m = reload(t, m)
	m = selectNamed(t, m, "router")
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "router  ping 192.168.1.1", "Status       UP   last check 4s ago (1ms)", "Interval     30s", "Timeout      2s", "Type         ICMP echo")
	requireAbsent(t, m, "Expected", "CERTIFICATE", " · 203")
	m, _ = press(t, m, keyEsc)
	m = selectNamed(t, m, "api-dns")
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "api-dns  DNS A api.example.com", "last check 40s ago (18ms) · 203.0.113.10 +1",
		"Interval     1m", "Timeout      5s", "Expected     203.0.113.10", "Alerting     after 2 failures")
	requireAbsent(t, m, "@", "ICMP", "CERTIFICATE")

	resolver := dnsFixture
	resolver.Name, resolver.Resolver, resolver.Expected = "nas", "[fd00::53]:53", ""
	_, m = probeModel(t, "", 100, 50, resolver)
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "nas  DNS A api.example.com @[fd00::53]:53", "Expected     any answer")
}

func TestDNSDetailMismatchSaysTheAnswerOnce(t *testing.T) {
	s, m := probeModel(t, "", 100, 50, dnsFixture)
	r := check.Result{CheckName: "api-dns", At: at("03:44:48"), Status: check.StatusDown, Latency: 19 * time.Millisecond,
		Answer: "203.0.113.9", Answers: 1, Error: "A = 203.0.113.9, want 203.0.113.10"}
	if err := s.Record(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	row := heartbeatRow(t, m, "api-dns")
	if strings.Count(row, "203.0.113.9") != 1 {
		t.Fatalf("row = %q, want the answer once", row)
	}
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "Last error   A = 203.0.113.9, want 203.0.113.10")
	if status := lineWith(view(m), "Status"); strings.Contains(status, "203.0.113") || !strings.Contains(status, "last check 12s ago (19ms)") {
		t.Fatalf("status line %q repeats the answer", status)
	}
}

func TestEditAndDuplicateKeepPingAndDNSFields(t *testing.T) {
	dns := dnsFixture
	dns.Resolver = "192.0.2.53:5353"
	s, m := probeModel(t, "", 100, 50, pingFixture, dns)
	m = selectNamed(t, m, "api-dns")
	m, _ = press(t, m, keyE)
	requireText(t, m, "Edit monitor", "Type: ‹ DNS › ", "Name: api-dns", "Domain: api.example.com", "Record: ‹ A ›",
		"Resolver: 192.0.2.53:5353", "Expected: 203.0.113.10", "Interval: 1m", "Alert after: ‹ 2 ›")
	m, _ = press(t, m, keyTab, keyTab, keyTab) // Record, Resolver, Expected
	m = replaceField(t, m, "203.0.113.10", "203.0.113.11")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	m = selectNamed(t, m, "api-dns")
	got, _ := storedByName(t, s, "api-dns")
	if got.Expected != "203.0.113.11" || got.Domain != "api.example.com" || got.Record != "A" || got.Resolver != "192.0.2.53:5353" || got.Interval != time.Minute {
		t.Fatalf("edited %+v", got)
	}

	m, _ = press(t, m, keyY)
	requireText(t, m, "New monitor · copy of api-dns", "Type: ‹ DNS › ", "Name: api-dns copy", "Domain: api.example.com", "Resolver: 192.0.2.53:5353")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	copied, ok := storedByName(t, s, "api-dns copy")
	if want := got; !ok || copied.Domain != want.Domain || copied.Record != want.Record || copied.Resolver != want.Resolver ||
		copied.Expected != want.Expected || copied.AlertAfter != want.AlertAfter {
		t.Fatalf("copy %+v, want the fields of %+v", copied, got)
	}

	m = selectNamed(t, m, "router")
	m, _ = press(t, m, keyE)
	requireText(t, m, "Type: ‹ Ping › ", "Host: 192.168.1.1", "Timeout: 2s")
	m, _ = press(t, m, keyEsc, keyY)
	requireText(t, m, "Name: router copy", "Host: 192.168.1.1")
	m, cmd = press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	if c, ok := storedByName(t, s, "router copy"); !ok || c.Kind != config.CheckPing || c.Host != "192.168.1.1" || c.Timeout != 2*time.Second {
		t.Fatalf("ping copy %+v, %v", c, ok)
	}
}

func TestPingAndDNSAt80x24(t *testing.T) {
	s, m := probeModel(t, config.PingDenied, 80, 24, pingFixture, dnsFixture)
	probeHistory(t, s, 2)
	m = reload(t, m)
	requireRow(t, heartbeatRow(t, m, "router"), "PENDING", "cannot send pings")
	requireRow(t, heartbeatRow(t, m, "api-dns"), "UP", "18ms")
	m = openKind(t, m, 4)
	requireText(t, m, "Type: ‹ DNS › ", "Resolver: system resolver", "Expected: any answer", dnsCopyLine, "esc cancel")
	requireWidth(t, m, 80)
	m, _ = press(t, m, keyArrowLeft)
	requireText(t, m, pingDeniedText)
	requireWidth(t, m, 80)
}
