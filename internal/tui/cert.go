package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/giulianotesta7/up/internal/config"
)

const day = 24 * time.Hour

// certLeft renders the time to expiry as "61d", "5h" or "expired".
func certLeft(remaining time.Duration) string {
	switch {
	case remaining <= 0:
		return "expired"
	case remaining < day:
		return fmt.Sprintf("%dh", max(1, int(remaining.Hours())))
	default:
		return fmt.Sprintf("%dd", int(remaining/day))
	}
}

// certStyle is green, amber under 14 days and red under 3.
func certStyle(remaining time.Duration) lipgloss.Style {
	switch {
	case remaining < 3*day:
		return downStyle
	case remaining < 14*day:
		return warnStyle
	default:
		return upStyle
	}
}

// renderCert is the detail block for an HTTPS monitor's certificate. After a
// failed check it says the data is the last known and when it was seen.
func renderCert(s Service, now time.Time) string {
	if !s.HasCert {
		return ""
	}
	cert := s.Cert
	remaining := cert.NotAfter.Sub(now)
	var b strings.Builder
	b.WriteString("TLS certificate")
	if s.HasResult && s.Latest.Status != "up" && cert.Observed.Before(s.Latest.At) {
		b.WriteString("  " + warnStyle.Render("last known, observed "+ago(now, cert.Observed)))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  Host        %s\n", cert.Host)
	fmt.Fprintf(&b, "  Issuer      %s\n", orDash(cert.Issuer))
	fmt.Fprintf(&b, "  Expires     %s\n", cert.NotAfter.UTC().Format("2006-01-02 15:04 UTC"))
	left := "expired"
	switch {
	case remaining >= day:
		left = plural(int(remaining/day), "day")
	case remaining > 0:
		left = plural(max(1, int(remaining.Hours())), "hour")
	}
	fmt.Fprintf(&b, "  Remaining   %s\n", certStyle(remaining).Render(left))
	fmt.Fprintf(&b, "  Alerts      %s\n", certAlerts(s.Check, cert))
	return b.String()
}

// certAlerts lists the warning thresholds as sent or pending.
func certAlerts(c config.Check, cert config.Cert) string {
	if c.Channel == "" {
		return helpStyle.Render("no channel: warnings are not sent")
	}
	parts := make([]string, 0, len(config.CertWarnDays))
	for _, threshold := range config.CertWarnDays {
		if cert.Notified != 0 && threshold >= cert.Notified {
			parts = append(parts, upStyle.Render(fmt.Sprintf("%dd sent", threshold)))
		} else {
			parts = append(parts, helpStyle.Render(fmt.Sprintf("%dd pending", threshold)))
		}
	}
	return strings.Join(parts, " · ")
}
