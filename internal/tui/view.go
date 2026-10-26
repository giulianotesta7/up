package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/giulianotesta7/up/internal/check"
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true)
	headerStyle  = lipgloss.NewStyle().Bold(true).Faint(true)
	upStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	downStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	pendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true)
	helpStyle    = lipgloss.NewStyle().Faint(true)
)

const timeLayout = "Jan 02 15:04:05"

func renderList(snap Snapshot, selected int, now time.Time, loadErr error) string {
	var b strings.Builder
	b.WriteString(renderTitle(snap) + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))
	b.WriteString("\n")

	if len(snap.Services) == 0 {
		b.WriteString("No checks configured.\n")
	} else {
		nameWidth := len("SERVICE")
		for _, s := range snap.Services {
			nameWidth = max(nameWidth, len(s.Check.Name))
		}
		row := func(marker, name, status, latency, last, reason string) string {
			return strings.TrimRight(fmt.Sprintf("%s %-*s  %-7s  %8s  %-10s  %s", marker, nameWidth, name, status, latency, last, reason), " ")
		}
		b.WriteString(headerStyle.Render(row(" ", "SERVICE", "STATUS", "LATENCY", "LAST CHECK", "")) + "\n")
		for i, s := range snap.Services {
			marker := " "
			if i == selected {
				marker = ">"
			}
			latency, last, reason := "", "", ""
			if s.HasResult {
				latency, last = formatLatency(s.Latest.Latency), ago(now, s.Latest.At)
				reason = s.Latest.Error
			}
			line := row(marker, s.Check.Name, "\x00", latency, last, reason)
			b.WriteString(strings.Replace(line, "\x00", styledLabel(s, 7), 1) + "\n")
		}
	}

	b.WriteString("\n" + helpStyle.Render("↑/↓ select (k/j) · enter details · n new monitor · e edit · d delete · q quit · ctrl+c quit"))
	return b.String()
}

func renderDetail(snap Snapshot, s Service, now time.Time, loadErr error) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(s.Check.Name) + "  " + s.Check.URL + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))
	b.WriteString("\n")

	fmt.Fprintf(&b, "Interval: %s   Timeout: %s   Expected status: %d\n", s.Check.Interval, s.Check.Timeout, s.Check.ExpectedStatus)
	fmt.Fprintf(&b, "Status: %s", styledLabel(s, 0))
	if s.HasResult {
		fmt.Fprintf(&b, "   Last check: %s (%s)", ago(now, s.Latest.At), formatLatency(s.Latest.Latency))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Last error: %s\n", orDash(s.Latest.Error))
	downSince := "—"
	if !s.DownSince.IsZero() {
		downSince = fmt.Sprintf("%s (%s)", formatTime(s.DownSince, now), ago(now, s.DownSince))
	}
	fmt.Fprintf(&b, "Down since: %s\n\n", downSince)

	if len(s.Recent) == 0 {
		b.WriteString("No checks yet.\n")
	} else {
		var strip strings.Builder
		for i := len(s.Recent) - 1; i >= 0; i-- {
			if s.Recent[i].Status == check.StatusUp {
				strip.WriteString(upStyle.Render("█"))
			} else {
				strip.WriteString(downStyle.Render("▁"))
			}
		}
		fmt.Fprintf(&b, "Recent checks (oldest → newest): %s\n", strip.String())
	}

	b.WriteString("\nRecent outages:\n")
	if len(s.Outages) == 0 {
		b.WriteString("  No outages recorded.\n")
	}
	for _, o := range s.Outages {
		if o.Ongoing {
			fmt.Fprintf(&b, "  %s → ongoing   %s\n", formatTime(o.Start, now), o.Error)
			continue
		}
		fmt.Fprintf(&b, "  %s → %s (%s)   %s\n", formatTime(o.Start, now), formatTime(o.End, now), o.End.Sub(o.Start), o.Error)
	}

	b.WriteString("\n" + helpStyle.Render("esc back · n new monitor · e edit · d delete · q quit · ctrl+c quit"))
	return b.String()
}

func renderTitle(snap Snapshot) string {
	var nUp, nDown, nPending int
	for _, s := range snap.Services {
		switch s.Label() {
		case "UP":
			nUp++
		case "DOWN":
			nDown++
		default:
			nPending++
		}
	}
	return titleStyle.Render("up") + fmt.Sprintf(" · %d services · %d up · %d down · %d pending", len(snap.Services), nUp, nDown, nPending)
}

func renderBanners(snap Snapshot, now time.Time, loadErr error) string {
	var b strings.Builder
	if snap.Stale(now) {
		last := "no heartbeat yet"
		if snap.HasBeat {
			last = "last heartbeat " + ago(now, snap.LastBeat)
		}
		b.WriteString(warnStyle.Render("daemon not responding: "+last+" — is `up serve` running?") + "\n")
	}
	if loadErr != nil {
		b.WriteString(warnStyle.Render("error reading the database: "+loadErr.Error()) + "\n")
	}
	return b.String()
}

// styledLabel colors the status label, padded to width before styling so
// that columns stay aligned.
func styledLabel(s Service, width int) string {
	label := fmt.Sprintf("%-*s", width, s.Label())
	switch s.Label() {
	case "UP":
		return upStyle.Render(label)
	case "DOWN":
		return downStyle.Render(label)
	default:
		return pendingStyle.Render(label)
	}
}

func formatLatency(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d < time.Millisecond:
		return "<1ms"
	default:
		return d.Round(time.Millisecond).String()
	}
}

// ago renders the age of t as "30s ago", "2m ago", "3h ago" or "2d ago".
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// formatTime shows t in the same location as now (the local zone when
// running).
func formatTime(t, now time.Time) string {
	return t.In(now.Location()).Format(timeLayout)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
