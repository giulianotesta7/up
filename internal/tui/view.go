package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const timeLayout = "Jan 02 15:04:05"

func renderList(snap Snapshot, selected int, now time.Time, loadErr error, width int) string {
	var b strings.Builder
	b.WriteString(renderBanners(snap, now, loadErr))

	if len(snap.Services) == 0 {
		b.WriteString(helpStyle.Render("No checks configured.") + "\n")
	} else {
		nameWidth := ansi.StringWidth("MONITOR")
		for _, s := range snap.Services {
			nameWidth = max(nameWidth, ansi.StringWidth(s.Check.Name))
		}
		// Drop the trend before it crowds the existing columns. Long names
		// also consume terminal cells, including double-width characters.
		statusWidth := 7
		for _, s := range snap.Services {
			statusWidth = max(statusWidth, ansi.StringWidth(s.Label()))
		}
		// MUTED is always shown (13 cells), so its place does not move when a
		// mute starts or ends.
		anyCert := false
		for _, s := range snap.Services {
			anyCert = anyCert || s.HasCert
		}
		certWidth := 0
		if anyCert {
			certWidth = 9
		}
		showTrend := width <= 0 || width >= max(78, nameWidth+55+statusWidth+certWidth)
		row := func(marker, dot, name, status, latency, trend, cert, last, muted, reason string) string {
			padding := strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name)))
			middle := ""
			if showTrend {
				middle = "  " + trend + strings.Repeat(" ", max(0, 16-ansi.StringWidth(trend)))
			}
			if anyCert {
				middle += "  " + cert + strings.Repeat(" ", max(0, 7-ansi.StringWidth(cert)))
			}
			prefix := marker + " " + dot + " " + name + padding + "  " + status + fmt.Sprintf("  %8s", latency) + middle
			last = fmt.Sprintf("%-10s  %s", last, muted+strings.Repeat(" ", max(0, 13-ansi.StringWidth(muted))))
			if showTrend {
				return prefix + "  " + strings.TrimRight(fmt.Sprintf("%-10s  %s", last, reason), " ")
			}
			return strings.TrimRight(prefix+fmt.Sprintf("  %-10s  %s", last, reason), " ")
		}
		b.WriteString(headerStyle.Render(row(" ", " ", "MONITOR", fmt.Sprintf("%-*s", statusWidth, "STATUS"), "LATENCY", "TREND (16)", "CERT", "LAST CHECK", "MUTED", "")) + "\n")
		for i, s := range snap.Services {
			marker := " "
			if i == selected {
				marker = accentStyle.Render("▌")
			}
			latency, last, reason := "", "", ""
			if s.HasResult {
				latency, last = formatLatency(s.Latest.Latency), ago(now, s.Latest.At)
				reason = s.Latest.Error
			}
			name := s.Check.Name
			if i == selected {
				name = selectedNameStyle.Render(name)
			}
			cert := ""
			if anyCert {
				cert = helpStyle.Render("—")
			}
			if s.HasCert {
				remaining := s.Cert.NotAfter.Sub(now)
				cert = certStyle(remaining).Render(certLeft(remaining))
			}
			muted := ""
			if s.Muted(now) {
				muted = warnStyle.Render("⏸ " + timeLeft(s.Mute.Until.Sub(now)) + " left")
			}
			b.WriteString(row(marker, statusStyle(s).Render("●"), name, styledLabel(s, statusWidth), latency, sparkline(s.Recent, 16), cert, last, muted, reason) + "\n")
		}
	}

	b.WriteString("\n" + renderHelp("↑/↓ select · enter open · n new · e edit · d delete · m mute · ? more · q quit"))
	return b.String()
}

func renderDetail(snap Snapshot, s Service, now time.Time, loadErr error, width int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(s.Check.Name) + "  " + s.Check.URL + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))

	fmt.Fprintf(&b, "Interval: %s   Timeout: %s   Expected status: %d\n", s.Check.Interval, s.Check.Timeout, s.Check.ExpectedStatus)
	channelName := s.Check.Channel
	if channelName == "" {
		channelName = "none"
	}
	fmt.Fprintf(&b, "Channel: %s", channelName)
	for _, c := range snap.Channels {
		if c.Name == s.Check.Channel && s.Check.Channel != "" {
			noun := "recipients"
			if len(c.Recipients) == 1 {
				noun = "recipient"
			}
			fmt.Fprint(&b, " "+helpStyle.Render(fmt.Sprintf("(%s via %s → %d %s)", c.Kind, c.SMTPServer, len(c.Recipients), noun)))
			break
		}
	}
	b.WriteString("\n")
	if s.Muted(now) {
		held := "nothing pending"
		switch s.Pending() {
		case "down":
			held = "1 email pending (down since " + formatTime(s.DownSince, now) + ")"
		case "recovery":
			held = "1 email pending (recovery)"
		}
		fmt.Fprintf(&b, "Muted: %s\n", warnStyle.Render(fmt.Sprintf("until %s (%s left)", s.Mute.Until.In(now.Location()).Format("15:04"), timeLeft(s.Mute.Until.Sub(now))))+" · "+held)
	}
	if !s.thresholdsDefault() {
		fmt.Fprintf(&b, "Alerting: after %s · recovers after %s\n", plural(s.Check.FailuresToAlert(), "failure"), plural(s.Check.SuccessesToRecover(), "success"))
	}
	fmt.Fprintf(&b, "Status: %s", styledLabel(s, 0))
	if s.HasResult {
		fmt.Fprintf(&b, "   Last check: %s (%s)", ago(now, s.Latest.At), formatLatency(s.Latest.Latency))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Last error: %s\n", orDash(s.Latest.Error))
	if streak := streakLine(s, now); streak != "" {
		b.WriteString(streak + "\n")
	}
	downSince := "—"
	if !s.DownSince.IsZero() {
		downSince = fmt.Sprintf("%s (%s)", formatTime(s.DownSince, now), ago(now, s.DownSince))
	}
	fmt.Fprintf(&b, "Down since: %s\n\n", downSince)

	if len(s.Recent) == 0 {
		b.WriteString("No checks yet.\n")
	} else {
		chartWidth := recentLimit
		if width > 0 {
			chartWidth = min(chartWidth, max(8, width-graphLabelWidth-1))
		}
		window := graphWindow(s.Recent, chartWidth)
		b.WriteString(strings.Join(chart(window, chartWidth, 4), "\n") + "\n")
		avg, p95, maximum, failed, total := latencyStats(window)
		if failed == total {
			fmt.Fprintf(&b, "no successful checks · %d failed of %d\n", failed, total)
		} else {
			fmt.Fprintf(&b, "avg %s · p95 %s · max %s · %d failed of %d\n", formatLatency(avg), formatLatency(p95), formatLatency(maximum), failed, total)
		}
	}

	if block := renderCert(s, now); block != "" {
		b.WriteString("\n" + block)
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

	b.WriteString("\n" + renderHelp("esc back · e edit · d delete · m mute · ? more · q quit"))
	return b.String()
}

func renderTitle(snap Snapshot) string { return renderTitleWidth(snap, 0) }

func renderTitleWidth(snap Snapshot, width int) string {
	line := titleStyle.Render(logoMark + " " + productName)
	if !snap.HasUptime {
		return line
	}
	percent := "100%"
	if snap.Uptime < 100 {
		percent = fmt.Sprintf("%.1f%%", math.Floor(snap.Uptime*10)/10)
	}
	style := downStyle
	if snap.Uptime >= 99.9 {
		style = upStyle
	} else if snap.Uptime >= 99 {
		style = slowStyle
	}
	line += "  " + style.Render(percent+" uptime today")
	if snap.Incidents > 0 {
		noun := "incidents"
		if snap.Incidents == 1 {
			noun = "incident"
		}
		extra := helpStyle.Render(fmt.Sprintf(" · %d %s", snap.Incidents, noun))
		if ansi.StringWidth(line+extra) <= ruleWidth(width) {
			line += extra
		}
	}
	return line
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
	return statusStyle(s).Render(fmt.Sprintf("%-*s", width, s.Label()))
}

func statusStyle(s Service) lipgloss.Style {
	label := s.Label()
	switch {
	case label == "UP":
		return upStyle
	case label == "DOWN":
		return downStyle
	case strings.HasPrefix(label, "FAILING"), strings.HasPrefix(label, "RECOVERING"):
		return warnStyle
	default:
		return pendingStyle
	}
}

func plural(n int, noun string) string {
	return fmt.Sprintf("%d %s", n, pluralNoun(n, noun))
}

// streakLine explains a FAILING or RECOVERING status: how many results in a
// row, since when, and how many more fire the alert.
func streakLine(s Service, now time.Time) string {
	var n int
	var noun string
	switch {
	case strings.HasPrefix(s.Label(), "FAILING"):
		n, noun = s.Streak.Failures, "failure"
	case strings.HasPrefix(s.Label(), "RECOVERING"):
		n, noun = s.Streak.Successes, "success"
	default:
		return ""
	}
	if n < 1 || n > len(s.Recent) {
		return ""
	}
	return fmt.Sprintf("Streak: %d consecutive %s since %s", n, pluralNoun(n, noun), formatTime(s.Recent[n-1].At, now))
}

func pluralNoun(n int, noun string) string {
	if n == 1 {
		return noun
	}
	if strings.HasSuffix(noun, "s") {
		return noun + "es"
	}
	return noun + "s"
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

// renderKeys is the full key map shown by "?" on the list and detail screens.
func renderKeys() string {
	row := func(key, left, key2, right string) string {
		cell := accentStyle.Render(key) + strings.Repeat(" ", max(1, 9-ansi.StringWidth(key))) + helpStyle.Render(left)
		if key2 == "" {
			return "  " + cell
		}
		pad := strings.Repeat(" ", max(1, 28-9-ansi.StringWidth(left)))
		return "  " + cell + pad + accentStyle.Render(key2) + strings.Repeat(" ", max(1, 4-ansi.StringWidth(key2))) + helpStyle.Render(right)
	}
	lines := []string{
		headerStyle.Render("MONITORS") + strings.Repeat(" ", 20) + headerStyle.Render("ALERTING"),
		"",
		row("↑/↓ k/j", "select", "m", "mute selected"),
		row("enter", "details", "u", "unmute"),
		row("n", "new monitor", "s", "SMTP servers"),
		row("e", "edit", "c", "channels"),
		row("d", "delete", "", ""),
		"",
		headerStyle.Render("VIEW"),
		"",
		row("a", "animation on/off", "", ""),
		row("q", "quit", "", ""),
		"",
		"  " + accentStyle.Render("? or esc") + "  " + helpStyle.Render("close"),
	}
	return strings.Join(lines, "\n")
}
