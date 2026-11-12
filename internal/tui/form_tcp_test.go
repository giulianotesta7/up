package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/config"
)

var keyArrowLeft = tea.KeyPressMsg{Code: tea.KeyLeft}

var tcpCheckFixture = config.Check{Name: "database", Kind: config.CheckTCP, Host: "db.example.com", Port: 5432, Interval: time.Minute, Timeout: 3 * time.Second}

// openTCPForm opens a new form and selects the TCP type (shift+tab from Name).
func openTCPForm(t *testing.T) (m Model) {
	t.Helper()
	_, m = formStore(t)
	m, _ = press(t, m, keyN, keyShiftTab, keyRight)
	return m
}

func TestNewFormOffersTypeSelectorDefaultingToHTTP(t *testing.T) {
	_, m := formStore(t)
	m, _ = press(t, m, keyN)
	out := view(m)
	requireText(t, m, "Type: ‹ HTTP › ", "HTTP/HTTPS URL", "Expected status", "TLS expiry")
	if strings.Index(out, "Type:") > strings.Index(out, "Name:") {
		t.Fatalf("Type must be the first field:\n%s", out)
	}
	if strings.Contains(out, "Port") || strings.Contains(out, "Host") {
		t.Fatalf("HTTP form shows TCP fields:\n%s", out)
	}
	// The cursor starts in Name, so typing still fills the name as before.
	m = typeText(t, m, "web")
	requireText(t, m, "Name: web")
}

func TestTCPFormShowsOnlyTCPFieldsAndCopy(t *testing.T) {
	m := openTCPForm(t)
	requireText(t, m, "Type: ‹ TCP › ", "Name", "Host", "Port", "Interval", "30s", "Timeout", "5s", "Channel", "Alert after", "Recover after",
		"Checks that the port accepts a connection.", "←/→ choose")
	out := view(m)
	for _, hidden := range []string{"HTTP/HTTPS URL", "Expected status", "TLS expiry", "HTTPS only", "Advanced"} {
		if strings.Contains(out, hidden) {
			t.Fatalf("TCP form shows %q:\n%s", hidden, out)
		}
	}
	m, _ = press(t, m, keyArrowLeft)
	requireText(t, m, "Type: ‹ HTTP › ", "HTTP/HTTPS URL", "Expected status")
}

func fillTCP(t *testing.T, m Model, name, host, port string) Model {
	t.Helper()
	m, _ = press(t, m, keyTab) // Name
	m = typeText(t, m, name)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, host)
	m, _ = press(t, m, keyTab)
	return typeText(t, m, port)
}

func TestTCPFormSavesOnlyTCPFields(t *testing.T) {
	s, m := formStore(t)
	m, _ = press(t, m, keyN, keyShiftTab, keyRight)
	m = fillTCP(t, m, "database", "::1", "5432")
	mm, cmd := press(t, m, keyEnter)
	finishCommand(t, mm, cmd)
	got, err := s.Checks(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("checks = %+v, %v", got, err)
	}
	c := got[0]
	if c.Kind != config.CheckTCP || c.Name != "database" || c.Host != "::1" || c.Port != 5432 || c.URL != "" || c.ExpectedStatus != 0 || c.TLSOff ||
		c.Interval != 30*time.Second || c.Timeout != 5*time.Second {
		t.Fatalf("saved = %+v, want a TCP check with only TCP fields", c)
	}
}

func TestTCPFormRejectsBadPortAndKeepsStoreEmpty(t *testing.T) {
	for _, tc := range []struct{ port, want string }{{"0", "port 0 must be between 1 and 65535"}, {"70000", "port 70000"}, {"ssh", "port:"}, {"", "port:"}} {
		s, m := formStore(t)
		m, _ = press(t, m, keyN, keyShiftTab, keyRight)
		m = fillTCP(t, m, "database", "db.example.com", tc.port)
		m, cmd := press(t, m, keyEnter)
		if cmd != nil {
			t.Fatalf("port %q saved", tc.port)
		}
		requireText(t, m, "Cannot save monitor", tc.want)
		if got, _ := s.Checks(context.Background()); len(got) != 0 {
			t.Fatalf("port %q stored %+v", tc.port, got)
		}
	}
}

func TestSwitchingTypeDropsHiddenValues(t *testing.T) {
	_, m := formStore(t)
	m, _ = press(t, m, keyN)
	m = typeText(t, m, "x")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "https://example.com")
	m, _ = press(t, m, keyShiftTab, keyShiftTab, keyRight) // Type -> TCP
	if out := view(m); strings.Contains(out, "example.com") {
		t.Fatalf("URL leaked into the TCP form:\n%s", out)
	}
	m, _ = press(t, m, keyTab, keyTab)
	m = typeText(t, m, "host.example.com")
	m, _ = press(t, m, keyShiftTab, keyShiftTab, keyArrowLeft) // Type -> HTTP
	if out := view(m); strings.Contains(out, "host.example.com") {
		t.Fatalf("host leaked into the HTTP form:\n%s", out)
	}
	requireText(t, m, "Expected status: 200")
}

func TestEditTCPOpensWithTCPSelectedAndSavesChanges(t *testing.T) {
	s, m := formStore(t)
	if err := s.AddCheck(context.Background(), tcpCheckFixture); err != nil {
		t.Fatal(err)
	}
	m = finishCommand(t, m, m.load)
	m, _ = press(t, m, keyE)
	requireText(t, m, "Edit monitor", "Type: ‹ TCP › ", "Host: db.example.com", "Port: 5432", "Timeout: 3s")
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "5432", "6543")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	got, _ := s.Checks(context.Background())
	if len(got) != 1 || got[0].Port != 6543 || got[0].Host != "db.example.com" || got[0].Kind != config.CheckTCP || got[0].Timeout != 3*time.Second {
		t.Fatalf("saved = %+v", got)
	}
}

func TestEditTCPSwitchedToHTTPCarriesNoHostOrPort(t *testing.T) {
	s, m := formStore(t)
	if err := s.AddCheck(context.Background(), tcpCheckFixture); err != nil {
		t.Fatal(err)
	}
	m = finishCommand(t, m, m.load)
	m, _ = press(t, m, keyE, keyShiftTab, keyArrowLeft)
	requireText(t, m, "Type: ‹ HTTP › ", "HTTP/HTTPS URL: ", "Expected status: 200")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "https://db.example.com/health")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	got, _ := s.Checks(context.Background())
	if len(got) != 1 || got[0].Kind != "" || got[0].URL != "https://db.example.com/health" || got[0].Host != "" || got[0].Port != 0 || got[0].ExpectedStatus != 200 {
		t.Fatalf("saved = %+v, want a plain HTTP check", got)
	}
}

func TestEditHTTPSwitchedToTCPCarriesNoHTTPFields(t *testing.T) {
	c := testChecks[0]
	c.Method, c.ExpectText = "POST", "pong"
	f := editMonitorForm(c, false)
	f.kind = kindTCP
	f.values[1], f.port = "db.example.com", "5432"
	got, err := f.check()
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != config.CheckTCP || got.URL != "" || got.ExpectedStatus != 0 || got.Method != "" || got.ExpectText != "" || got.TLSOff {
		t.Fatalf("check() = %+v, want only TCP fields", got)
	}
}

func TestTCPListAndDetailShowTarget(t *testing.T) {
	snap := Snapshot{Services: []Service{
		{Check: testChecks[0], HasCert: true, Cert: config.Cert{Check: "api", NotAfter: now.Add(30 * 24 * time.Hour)}},
		{Check: tcpCheckFixture},
	}}
	list := ansi.Strip(renderList(snap, 0, now, nil, 120))
	for _, line := range strings.Split(list, "\n") {
		if strings.Contains(line, "database") {
			if !strings.Contains(line, "tcp") || strings.Contains(line, "—") {
				t.Fatalf("tcp row = %q, want a tcp marker and no cert value", line)
			}
		}
		if strings.Contains(line, "api") && strings.Contains(line, "tcp") {
			t.Fatalf("http row = %q has a tcp marker", line)
		}
	}
	detail := ansi.Strip(renderDetail(snap, snap.Services[1], now, nil, 100))
	for _, want := range []string{"database  db.example.com:5432", "TCP connect"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "Expected") || strings.Contains(detail, "CERTIFICATE") {
		t.Fatalf("detail shows HTTP-only rows:\n%s", detail)
	}
	httpDetail := ansi.Strip(renderDetail(snap, snap.Services[0], now, nil, 100))
	if !strings.Contains(httpDetail, "api  https://api.example.com/health") || !strings.Contains(httpDetail, "Expected") {
		t.Fatalf("http detail changed:\n%s", httpDetail)
	}
}

func TestHTTPListHasNoTCPMarker(t *testing.T) {
	list := ansi.Strip(renderList(seededSnapshot(t, now), 0, now, nil, 120))
	if strings.Contains(list, "tcp") {
		t.Fatalf("http-only list shows a tcp marker:\n%s", list)
	}
}
