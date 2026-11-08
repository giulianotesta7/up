package tui

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/config"
)

const timeLayout = "Jan 02 15:04:05"

func renderList(snap Snapshot, selected int, now time.Time, loadErr error, width int, filters ...listFilter) string {
	var b strings.Builder
	b.WriteString(renderBanners(snap, now, loadErr))
	var filter listFilter
	if len(filters) > 0 {
		filter = filters[0]
	}
	// No match keeps the table header and says so on the first row's line.
	noMatch := len(snap.Services) == 0 && filter.active() && filter.total > 0
	if len(snap.Services) == 0 && !noMatch {
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
			statusWidth = max(statusWidth, ansi.StringWidth(snap.shown(s).Label()))
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
		heldWidth, heldHeader := heldColumn(snap, now)
		showTrend := width <= 0 || width >= max(78, nameWidth+55+statusWidth+certWidth)
		// A tcp or hb tag follows the name when the list has room for it.
		tcpTag := kindTagWidth(snap, width, nameWidth, statusWidth, certWidth)
		nameWidth += tcpTag
		row := func(marker, dot, name, status, latency, trend, cert, last, muted, reason string) string {
			padding := strings.Repeat(" ", max(0, nameWidth-ansi.StringWidth(name)))
			middle := ""
			if showTrend {
				middle = "  " + trend + strings.Repeat(" ", max(0, 16-ansi.StringWidth(trend)))
			}
			if anyCert {
				middle += "  " + cert + strings.Repeat(" ", max(0, 7-ansi.StringWidth(cert)))
			}
			prefix := marker + " " + dot + " " + name + padding + "  " + status + "  " + strings.Repeat(" ", max(0, 8-ansi.StringWidth(latency))) + latency + middle
			last = fmt.Sprintf("%-10s  %s", last, muted+strings.Repeat(" ", max(0, heldWidth-ansi.StringWidth(muted))))
			if showTrend {
				return prefix + "  " + strings.TrimRight(fmt.Sprintf("%-10s  %s", last, reason), " ")
			}
			return strings.TrimRight(prefix+fmt.Sprintf("  %-10s  %s", last, reason), " ")
		}
		b.WriteString(headerStyle.Render(row(" ", " ", "MONITOR", fmt.Sprintf("%-*s", statusWidth, "STATUS"), "LATENCY", "TREND (16)", "CERT", "LAST CHECK", heldHeader, "")) + "\n")
		for i, s := range snap.Services {
			marker := " "
			if i == selected {
				marker = accentStyle.Render("▌")
			}
			s = snap.shown(s)
			latency, last, reason := "", "", ""
			if s.HasResult {
				latency, last = formatLatency(s.Latest.Latency), ago(now, s.Latest.At)
				reason = s.Latest.Error
				if answer := dnsAnswer(s.Latest); reason == "" && answer != "" {
					reason = helpStyle.Render(s.Check.Record + " " + answer)
				}
			}
			trend := sparkline(s.Recent, 16)
			if snap.Pings.Holds(s.Check) {
				trend, reason = "", warnStyle.Render(config.PingDeniedReason)
			}
			if s.Check.Kind == kindHeartbeat {
				latency, last, reason = helpStyle.Render("—"), heartbeatLast(s, now), heartbeatReason(s, snap, now)
				trend = pingCells(s.Recent, 16, false)
			}
			name := s.Check.Name
			if i == selected {
				name = selectedNameStyle.Render(name)
			}
			if filter.query != "" {
				base := lipgloss.NewStyle()
				if i == selected {
					base = selectedNameStyle
				}
				name = highlightMatch(name, filter.query, base)
			}
			name = tagKind(name, s.Check, nameWidth, tcpTag)
			cert := ""
			if anyCert && (s.Check.Kind == "" || s.Check.Kind == config.CheckHTTP) {
				cert = helpStyle.Render("—")
			}
			if s.HasCert {
				remaining := s.Cert.NotAfter.Sub(now)
				cert = certStyle(remaining).Render(certLeft(remaining))
			}
			muted := heldCell(s, now)
			b.WriteString(row(marker, statusStyle(s).Render("●"), name, styledLabel(s, statusWidth), latency, trend, cert, last, muted, reason) + "\n")
		}
		if noMatch {
			b.WriteString("  " + noMatchLine + "\n")
		}
	}

	b.WriteString("\n" + footerFor(filter, width))
	return b.String()
}

type detailOptions struct {
	compact                 bool
	config, cert, incidents bool
	chartRows               int
	// tight drops the blank lines between sections; hideStats drops the
	// latency numbers under the chart.
	tight, hideStats bool
	// incidentRows and alertRows are the most incidents and recent alerts
	// shown; cursor and offset place the incident window (see
	// incidentWindow).
	incidentRows, alertRows int
	cursor, offset          int
	// ping is the ping URL state of a heartbeat.
	ping pingView
}

func fullDetail() detailOptions {
	return detailOptions{config: true, cert: true, incidents: true, chartRows: 5, incidentRows: 6, alertRows: 3}
}

func renderDetail(snap Snapshot, s Service, now time.Time, loadErr error, width int) string {
	return renderDetailWithOptions(snap, s, now, loadErr, width, fullDetail())
}

// shrink takes the next step that makes the detail shorter, in this order:
// a 3-row chart, no CONFIG, no CERTIFICATE, no blank lines, a 2-row chart, no
// latency numbers, fewer incident rows (down to 3), fewer recent alerts (down
// to 1), fewer incident rows (down to 1). None of them depends on the
// incident scroll position, so scrolling never moves the screen. It reports
// false when nothing is left to take.
func (o *detailOptions) shrink() bool {
	switch {
	case o.chartRows > 3:
		o.chartRows = 3
	case o.config:
		o.config = false
	case o.cert:
		o.cert = false
	case !o.tight:
		o.tight = true
	case o.chartRows > 2:
		o.chartRows = 2
	case !o.hideStats:
		o.hideStats = true
	case o.incidentRows > 3:
		o.incidentRows--
	case o.alertRows > 1:
		o.alertRows--
	case o.incidentRows > 1:
		o.incidentRows--
	default:
		return false
	}
	return true
}

// fitDetail renders the detail with opts shrunk until it has at most height
// lines; a nonpositive height keeps opts. It returns the options used.
func fitDetail(snap Snapshot, s Service, now time.Time, loadErr error, width, height int, opts detailOptions) (string, detailOptions) {
	for {
		detail := renderDetailWithOptions(snap, s, now, loadErr, width, opts)
		if height <= 0 || strings.Count(detail, "\n")+1 <= height || !opts.shrink() {
			return detail, opts
		}
	}
}

// detailHelp is the detail's key help; it mentions scrolling when there are
// more incidents than rows.
func detailHelp(s Service, opts detailOptions) string {
	help := "esc back · e edit · d delete · m mute · ? more · q quit"
	scroll := opts.incidents && len(s.Incidents) > opts.incidentRows
	if s.Check.Kind == kindHeartbeat {
		return heartbeatHelp(help, scroll, opts.ping)
	}
	if scroll {
		help = "↑/↓ scroll incidents · " + help
	}
	return help
}

func renderDetailWithOptions(snap Snapshot, s Service, now time.Time, loadErr error, width int, opts detailOptions) string {
	var b strings.Builder
	held := snap.Pings.Holds(s.Check)
	s = snap.shown(s)
	heartbeat := s.Check.Kind == kindHeartbeat
	target := s.Check.Target()
	switch s.Check.Kind {
	case kindHeartbeat:
		target = heartbeatTarget(s.Check)
	case kindPing, kindDNS:
		target = probeTarget(s.Check)
	}
	b.WriteString(titleStyle.Render(s.Check.Name) + "  " + target + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))

	// Sections are separated by one blank line; a header sits right above its rows.
	blank := "\n"
	if opts.tight {
		blank = ""
	}
	section := func(title string) {
		b.WriteString(blank + headerStyle.Render(title) + "\n")
	}
	field := func(label, value string) {
		fmt.Fprintf(&b, "  %-11s  %s\n", label, value)
	}

	section("STATUS")
	if heartbeat {
		heartbeatStatus(field, s, now)
	} else {
		status := styledLabel(s, 0)
		if s.HasResult {
			status += "   last check " + ago(now, s.Latest.At) + " (" + formatLatency(s.Latest.Latency) + ")"
			// A mismatch already names the answer in Last error.
			if answer := dnsAnswer(s.Latest); answer != "" && s.Latest.Error == "" {
				status += " · " + answer
			}
		}
		field("Status", status)
	}
	if s.Muted(now) {
		held := "nothing pending"
		switch s.Pending() {
		case "down":
			held = "1 alert pending (down since " + formatTime(s.DownSince, now) + ")"
		case "recovery":
			held = "1 alert pending (recovery)"
		}
		field("Muted", warnStyle.Render(fmt.Sprintf("until %s (%s left)", s.Mute.Until.In(now.Location()).Format("15:04"), timeLeft(s.Mute.Until.Sub(now))))+" · "+held)
	}
	if s.InMaintenance.Active {
		held := "nothing pending"
		switch s.heldPending() {
		case "down":
			held = "1 alert pending (down since " + formatTime(s.DownSince, now) + ")"
		case "recovery":
			held = "1 alert pending (recovery)"
		}
		field("Maintenance", warnStyle.Render(fmt.Sprintf("until %s (%s left)", clockWithDay(s.InMaintenance.End, now), timeLeft(s.InMaintenance.End.Sub(now))))+" · "+held)
	}
	if held {
		field("Last error", warnStyle.Render(config.PingDeniedReason)+helpStyle.Render(" · README › Ping"))
	} else {
		field("Last error", orDash(s.Latest.Error))
	}
	if streak := streakLine(s, now); streak != "" {
		field("Streak", streak)
	}
	downSince := "—"
	if !s.DownSince.IsZero() {
		downSince = fmt.Sprintf("%s (%s)", formatTime(s.DownSince, now), ago(now, s.DownSince))
	}
	field("Down since", downSince)

	if opts.config {
		section("CONFIG")
		switch s.Check.Kind {
		case kindHeartbeat:
			field("Period", shortDuration(s.Check.Period))
			field("Grace", shortDuration(s.Check.Grace))
		case kindTCP:
			field("Interval", shortDuration(s.Check.Interval))
			field("Timeout", shortDuration(s.Check.Timeout))
			field("Type", "TCP connect")
		case kindPing:
			field("Interval", shortDuration(s.Check.Interval))
			field("Timeout", shortDuration(s.Check.Timeout))
			field("Type", "ICMP echo")
		case kindDNS:
			field("Interval", shortDuration(s.Check.Interval))
			field("Timeout", shortDuration(s.Check.Timeout))
			expected := s.Check.Expected
			if expected == "" {
				expected = helpStyle.Render(expectedHint)
			}
			field("Expected", expected)
		default:
			field("Interval", shortDuration(s.Check.Interval))
			field("Timeout", shortDuration(s.Check.Timeout))
			field("Expected", fmt.Sprint(s.Check.ExpectedStatus))
			renderRequestFields(field, s.Check, width)
		}
		channel := s.Check.Channel
		if channel == "" {
			channel = "none"
		}
		for _, c := range snap.Channels {
			if c.Name == s.Check.Channel && s.Check.Channel != "" {
				channel += " " + helpStyle.Render(channelSummary(c))
				break
			}
		}
		field("Channel", channel)
		if n := len(s.Maintenance); n > 0 {
			field("Maintenance", plural(n, "window")+helpStyle.Render(" (w to edit)"))
		}
		failure, success := "failure", "success"
		if heartbeat {
			failure, success = "missed ping", "ping"
		}
		if !s.thresholdsDefault() {
			field("Alerting", fmt.Sprintf("after %s · recovers after %s", plural(s.Check.FailuresToAlert(), failure), plural(s.Check.SuccessesToRecover(), success)))
		}
	}

	switch {
	case heartbeat:
		renderPings(&b, s, width, opts, blank)
		renderPingURL(&b, field, snap, width, opts.ping, blank)
	case len(s.Recent) == 0:
		section("LATENCY")
		b.WriteString("  No checks yet.\n")
	default:
		section("LATENCY")
		chartWidth := recentLimit
		if width > 0 {
			padding := 0
			if opts.compact {
				padding = 2
			}
			chartWidth = min(chartWidth, max(8, width-graphLabelWidth-1-padding))
		}
		window := graphWindow(s.Recent, chartWidth)
		b.WriteString("  " + strings.Join(chart(window, chartWidth, opts.chartRows), "\n  ") + "\n")
		avg, p95, maximum, failed, total := latencyStats(window)
		switch {
		case opts.hideStats:
		case failed == total:
			fmt.Fprintf(&b, "%s  no successful checks · %d failed of %d\n", blank, failed, total)
		default:
			_, hi, _ := chartScale(window)
			b.WriteString(blank + renderLatencyStats(avg, p95, maximum, hi, failed, total))
		}
	}

	if opts.cert {
		if block := renderCert(s, now); block != "" {
			b.WriteString(blank + block)
		}
	}

	if opts.incidents {
		b.WriteString(blank + strings.Join(renderIncidents(s, now, width, opts.incidentRows, opts.cursor, opts.offset), "\n") + "\n")
		b.WriteString(blank + strings.Join(renderRecentAlerts(s, now, width, opts.alertRows), "\n") + "\n")
	}
	b.WriteString(blank + renderHelp(detailHelp(s, opts)))
	return b.String()
}

func renderTitle(snap Snapshot) string { return renderTitleWidth(snap, 0) }

func renderTitleWidth(snap Snapshot, width int) string {
	line := titleStyle.Render(logoMark + " " + productName)
	if !snap.HasUptime {
		return line
	}
	percent := uptimePercent(snap.Uptime)
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

// uptimePercent rounds down to one decimal, so a monitor that was down at
// all never reads 100%.
func uptimePercent(p float64) string {
	if p >= 100 {
		return "100%"
	}
	return fmt.Sprintf("%.1f%%", math.Floor(p*10)/10)
}

// serverError marks a failed read from a remote server, so the banner names
// the server instead of a database that is not on this machine.
type serverError struct{ err error }

func (e serverError) Error() string { return e.err.Error() }
func (e serverError) Unwrap() error { return e.err }

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
		prefix := "error reading the database: "
		var remote serverError
		if errors.As(loadErr, &remote) {
			prefix = "cannot reach the server: "
		}
		b.WriteString(warnStyle.Render(prefix+loadErr.Error()) + "\n")
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
	return fmt.Sprintf("%d consecutive %s since %s", n, pluralNoun(n, noun), formatTime(s.Recent[n-1].At, now))
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
func renderKeys(remote ...bool) string {
	row := func(key, left, key2, right string) string {
		cell := accentStyle.Render(key) + strings.Repeat(" ", max(1, 9-ansi.StringWidth(key))) + helpStyle.Render(left)
		if key2 == "" {
			return "  " + cell
		}
		pad := strings.Repeat(" ", max(1, 28-9-ansi.StringWidth(left)))
		return "  " + cell + pad + accentStyle.Render(key2) + strings.Repeat(" ", max(1, 4-ansi.StringWidth(key2))) + helpStyle.Render(right)
	}
	retryKey, retryDescription := "", ""
	if len(remote) > 0 && remote[0] {
		retryKey, retryDescription = "r", "retry"
	}
	lines := []string{
		headerStyle.Render("MONITORS") + strings.Repeat(" ", 20) + headerStyle.Render("ALERTING"),
		"",
		row("↑/↓ k/j", "select", "m", "mute selected"),
		row("enter", "details", "u", "unmute"),
		row("n", "new monitor", "s", "mail servers"),
		row("e", "edit", "c", "channels"),
		row("y", "duplicate", "w", "maintenance"),
		row("d", "delete", "f", "defaults"),
		row("/", "search", "", ""),
		row("pgup/dn", "scroll incidents", "", ""),
		row("v", "ping URL", "", ""),
		"",
		headerStyle.Render("NOTIFICATIONS (s, c)"),
		"",
		row("tab", "next tab", "/", "search deliveries"),
		row("enter", "channel deliveries", "esc", "back"),
		"",
		headerStyle.Render("VIEW"),
		"",
		row("a", "animation on/off", "", ""),
		row("q", "quit", retryKey, retryDescription),
		"",
		"  " + accentStyle.Render("? or esc") + "  " + helpStyle.Render("close"),
	}
	return strings.Join(lines, "\n")
}

// renderLatencyStats lays the window's numbers out as a small table: dim
// headers over the values, in fixed columns. A maximum above the chart scale is
// amber, like the clipped bars.
func renderLatencyStats(avg, p95, maximum, scaleTop time.Duration, failed, total int) string {
	const column = 9
	pad := func(text string) string {
		return text + strings.Repeat(" ", max(1, column-ansi.StringWidth(text)))
	}
	maxStyle := lipgloss.NewStyle()
	if maximum > scaleTop {
		maxStyle = slowStyle
	}
	failedStyle := upStyle
	if failed > 0 {
		failedStyle = downStyle
	}
	header := "  " + headerStyle.Render(pad("AVG")+pad("P95")+pad("MAX")+"FAILED")
	values := "  " + pad(formatLatency(avg)) + pad(formatLatency(p95)) + maxStyle.Render(pad(formatLatency(maximum))) +
		failedStyle.Render(fmt.Sprint(failed)) + helpStyle.Render(fmt.Sprintf(" of %d", total))
	return header + "\n" + values + "\n"
}
