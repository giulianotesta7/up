package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/store"
)

// minuteLayout is the time format of the incident and alert lists: no
// seconds.
const minuteLayout = "Jan 02 15:04"

// incidentReasonWidth caps the FIRST FAILED CHECK column.
const incidentReasonWidth = 30

// incidentWindow keeps cursor inside the n incidents and the window of rows
// starting at offset around it. It returns both, adjusted.
func incidentWindow(n, rows, cursor, offset int) (int, int) {
	if n == 0 {
		return 0, 0
	}
	rows = max(1, min(rows, n))
	cursor = min(max(cursor, 0), n-1)
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+rows {
		offset = cursor - rows + 1
	}
	return cursor, min(max(offset, 0), n-rows)
}

// outcomeCell is an outcome with its mark and color; none is a dim dash.
func outcomeCell(o store.Outcome) string {
	switch o {
	case store.OutcomeSent:
		return upStyle.Render("✓ sent")
	case store.OutcomeFailed:
		return downStyle.Render("✗ failed")
	case store.OutcomeHeld:
		return warnStyle.Render("◷ held")
	}
	return helpStyle.Render("— none")
}

// alertedCell names the channel with the worst outcome when channels
// disagreed; otherwise a held alert says why.
func alertedCell(i Incident) string {
	cell := outcomeCell(i.Alert)
	switch {
	case i.Channel != "":
		cell += " " + i.Channel
	case i.Alert == store.OutcomeHeld && i.Reason != "":
		cell += " " + i.Reason
	}
	return cell
}

func incidentDuration(i Incident, now time.Time) string {
	if i.Ongoing {
		return "ongoing " + outageDuration(now.Sub(i.Start))
	}
	return outageDuration(i.End.Sub(i.Start))
}

// incidentSummary is the period, the incident count and, when the monitor
// was observed, its downtime and uptime.
func incidentSummary(s Service, uptime bool) string {
	text := fmt.Sprintf("%dd · %s", int(IncidentPeriod/day), plural(s.PeriodIncidents, "incident"))
	if s.PeriodObserved <= 0 {
		return text
	}
	if down := s.PeriodObserved - s.PeriodUp; down > 0 {
		text += " · " + outageDuration(down) + " down"
	} else {
		text += " · no downtime"
	}
	if uptime {
		text += " · " + uptimePercent(float64(s.PeriodUp)/float64(s.PeriodObserved)*100) + " uptime"
	}
	return text
}

// renderIncidents is the INCIDENTS section: a header with the period's
// numbers and the scroll position, the column titles and rows incidents from
// the window around cursor. Column widths come from every incident, so they
// do not change while scrolling. Lines are cut to width when it is known.
func renderIncidents(s Service, now time.Time, width, rows, cursor, offset int) []string {
	n := len(s.Incidents)
	rows = min(rows, n)
	cursor, offset = incidentWindow(n, rows, cursor, offset)
	hint := ""
	if n > rows {
		arrows := ""
		if offset > 0 {
			arrows += "↑"
		}
		if offset+rows < n {
			arrows += "↓"
		}
		hint = fmt.Sprintf("%d–%d of %d %s", offset+1, offset+rows, n, arrows)
	}
	title := headerStyle.Render("INCIDENTS")
	header := title + "  " + helpStyle.Render(incidentSummary(s, true))
	if hint != "" {
		header += "   " + helpStyle.Render(hint)
		// A narrow pane keeps the scroll position and drops the uptime.
		if width > 0 && ansi.StringWidth(header) > width {
			header = title + "  " + helpStyle.Render(incidentSummary(s, false)) + "   " + helpStyle.Render(hint)
		}
	}
	lines := []string{header}
	if n == 0 {
		return append(lines, "  No incidents.")
	}
	durationWidth, reasonWidth := ansi.StringWidth("DURATION"), ansi.StringWidth("FIRST FAILED CHECK")
	alertedWidth := 0
	for _, i := range s.Incidents {
		durationWidth = max(durationWidth, ansi.StringWidth(incidentDuration(i, now)))
		reasonWidth = max(reasonWidth, ansi.StringWidth(i.Error))
		alertedWidth = max(alertedWidth, ansi.StringWidth(alertedCell(i)))
	}
	reasonWidth = min(reasonWidth, incidentReasonWidth)
	if width > 0 {
		fixed := 2 + len(minuteLayout) + 2 + durationWidth + 2 + 2
		reasonWidth = max(min(reasonWidth, width-fixed-alertedWidth), ansi.StringWidth("FIRST FAILED"))
	}
	pad := func(text string, width int) string {
		text = ansi.Truncate(text, width, "…")
		return text + strings.Repeat(" ", max(0, width-ansi.StringWidth(text)))
	}
	cut := func(line string) string {
		if width > 0 {
			return ansi.Truncate(line, width, "")
		}
		return line
	}
	lines = append(lines, cut(headerStyle.Render("  "+pad("START", len(minuteLayout))+"  "+pad("DURATION", durationWidth)+"  "+pad("FIRST FAILED CHECK", reasonWidth)+"  ALERTED")))
	for k := offset; k < offset+rows; k++ {
		i := s.Incidents[k]
		marker := "  "
		if k == cursor {
			marker = accentStyle.Render("▌") + " "
		}
		lines = append(lines, cut(marker+i.Start.In(now.Location()).Format(minuteLayout)+"  "+pad(incidentDuration(i, now), durationWidth)+"  "+pad(i.Error, reasonWidth)+"  "+alertedCell(i)))
	}
	return lines
}

// renderRecentAlerts is the RECENT ALERTS section: the monitor's newest
// delivery attempts, at most rows of them.
func renderRecentAlerts(s Service, now time.Time, width, rows int) []string {
	lines := []string{headerStyle.Render("RECENT ALERTS")}
	shown := s.Attempts[:min(rows, len(s.Attempts))]
	if len(shown) == 0 {
		return append(lines, "  No alerts yet.")
	}
	channelWidth := 0
	for _, a := range shown {
		channelWidth = max(channelWidth, ansi.StringWidth(a.Channel))
	}
	eventWidth := len(store.EventRecovered)
	for _, a := range shown {
		outcome := outcomeCell(a.Outcome)
		line := "  " + a.At.In(now.Location()).Format(minuteLayout) + "  " + string(a.Event) + strings.Repeat(" ", max(0, eventWidth-len(a.Event))) +
			"  " + a.Channel + strings.Repeat(" ", channelWidth-ansi.StringWidth(a.Channel)) + "  " + outcome
		if a.Reason != "" {
			line += strings.Repeat(" ", max(0, 8-ansi.StringWidth(outcome))) + "  " + a.Reason
		}
		if width > 0 {
			line = ansi.Truncate(line, width, "")
		}
		lines = append(lines, line)
	}
	return lines
}
