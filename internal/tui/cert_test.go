package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

// certModel stores two monitors: web (HTTPS, with a certificate) and intranet
// (plain HTTP, none), and loads them back.
func certSnapshot(t *testing.T, cert config.Cert, webStatus check.Status) Snapshot {
	t.Helper()
	s, _ := formStore(t)
	ctx := context.Background()
	for _, c := range []config.Check{
		{Name: "web", URL: "https://example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200, Channel: cert.Check},
		{Name: "intranet", URL: "http://intranet.local", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200},
	} {
		if c.Channel != "" {
			if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "smtp", Host: "example.com", Port: 25, TLS: config.TLSNone, Sender: "a@example.com"}); err != nil {
				t.Fatal(err)
			}
			if err := s.AddChannel(ctx, config.Channel{Name: "ops", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
				t.Fatal(err)
			}
			c.Channel = "ops"
		}
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []check.Result{
		{CheckName: "web", At: now.Add(-time.Minute), Status: webStatus, Latency: time.Millisecond},
		{CheckName: "intranet", At: now.Add(-time.Minute), Status: check.StatusUp, Latency: time.Millisecond},
	} {
		if err := s.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	cert.Check = "web"
	if err := s.SaveCert(ctx, cert); err != nil {
		t.Fatal(err)
	}
	if err := s.Beat(ctx, now); err != nil {
		t.Fatal(err)
	}
	snap, err := Load(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestListShowsCertificateDays(t *testing.T) {
	cert := config.Cert{Check: "x", Host: "example.com", Issuer: "Let's Encrypt R11", Serial: "01", NotAfter: now.Add(6*day + 21*time.Hour), Observed: now}
	snap := certSnapshot(t, cert, check.StatusUp)
	list := ansi.Strip(renderList(snap, 0, now, nil, 120))
	header := strings.Split(list, "\n")[0]
	if !strings.Contains(header, "CERT") {
		t.Fatalf("no CERT column:\n%s", list)
	}
	for _, line := range strings.Split(list, "\n") {
		switch {
		case strings.Contains(line, "web"):
			if !strings.Contains(line, "6d") {
				t.Fatalf("web row: %q", line)
			}
		case strings.Contains(line, "intranet"):
			if !strings.Contains(line, "—") || strings.Contains(line, "6d") {
				t.Fatalf("intranet row: %q", line)
			}
		}
	}
	for remaining, want := range map[time.Duration]string{61*day + time.Hour: "61d", 5 * time.Hour: "5h", -time.Hour: "expired", 90 * time.Minute: "1h"} {
		if got := certLeft(remaining); got != want {
			t.Errorf("certLeft(%v) = %q, want %q", remaining, got, want)
		}
	}
}

func TestListWithoutCertificatesKeepsLayout(t *testing.T) {
	list := ansi.Strip(renderList(seededSnapshot(t, now), 0, now, nil, 120))
	if strings.Contains(list, "CERT") {
		t.Fatalf("CERT column without certificates:\n%s", list)
	}
}

func TestDetailShowsCertificateBlock(t *testing.T) {
	cert := config.Cert{Check: "x", Host: "example.com", Issuer: "Let's Encrypt R11", Serial: "01", NotAfter: time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC), Observed: now, Notified: 7}
	snap := certSnapshot(t, cert, check.StatusUp)
	detail := ansi.Strip(renderDetail(snap, snap.Services[0], now, nil, 100))
	for _, want := range []string{
		"TLS certificate\n", "  Host        example.com", "  Issuer      Let's Encrypt R11",
		"  Expires     2026-10-30 10:00 UTC", "  Remaining   5 days",
		"  Alerts      14d sent · 7d sent · 3d pending",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "last known") {
		t.Fatalf("fresh certificate marked stale:\n%s", detail)
	}
	plain := ansi.Strip(renderDetail(snap, snap.Services[1], now, nil, 100))
	if strings.Contains(plain, "TLS certificate") {
		t.Fatalf("plain HTTP monitor shows a certificate:\n%s", plain)
	}
}

func TestDetailMarksStaleAndExpiredCertificate(t *testing.T) {
	cert := config.Cert{Host: "example.com", Issuer: "CA", Serial: "01", NotAfter: now.Add(-time.Hour), Observed: now.Add(-4 * time.Minute)}
	snap := certSnapshot(t, cert, check.StatusDown)
	detail := ansi.Strip(renderDetail(snap, snap.Services[0], now, nil, 100))
	for _, want := range []string{"TLS certificate  last known, observed 4m ago", "Remaining   expired"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q:\n%s", want, detail)
		}
	}
}
