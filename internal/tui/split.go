package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const minDetailWidth = 32

// Measure the narrow table header, not variable-length error messages or banners.
func splitListWidth(snap Snapshot, selected int, now time.Time) int {
	list := renderList(snap, selected, now, nil, 1)
	for _, line := range strings.Split(list, "\n") {
		plain := ansi.Strip(line)
		if strings.Contains(plain, "MONITOR") && strings.Contains(plain, "LAST CHECK") {
			// The header ends where its last title does, but a held cell
			// ("◷ until 05:00") is wider than that title: keep it whole.
			width := ansi.StringWidth(line)
			held := 0
			for _, s := range snap.Services {
				held = max(held, ansi.StringWidth(heldCell(s, now)))
			}
			if held > 0 {
				_, header := heldColumn(snap, now)
				width = max(width, ansi.StringWidth(plain[:strings.LastIndex(plain, header)])+held)
			}
			return width
		}
	}
	return ansi.StringWidth("No checks configured.")
}

func (m Model) splitMode() bool {
	return m.width >= 110 && m.height >= 30 && m.width >= splitListWidth(m.snap, m.selected, m.now())+3+minDetailWidth
}

type activityEvent struct {
	at                  time.Time
	monitor, text, kind string
}

// activity derives the feed from the latest snapshot; certificate observations
// are timestamped when the certificate was observed, not when it expires.
func activity(snap Snapshot, now time.Time) []activityEvent {
	var events []activityEvent
	for _, svc := range snap.Services {
		for _, o := range svc.Outages {
			text := "went DOWN"
			if o.Error != "" {
				text += " " + o.Error
			}
			events = append(events, activityEvent{o.Start, svc.Check.Name, text, "down"})
			if !o.Ongoing {
				events = append(events, activityEvent{o.End, svc.Check.Name, "recovered after " + outageDuration(o.End.Sub(o.Start)), "up"})
			}
		}
		if svc.HasCert && svc.Cert.NotAfter.Sub(now) < 30*day {
			events = append(events, activityEvent{svc.Cert.Observed, svc.Check.Name, fmt.Sprintf("certificate expires in %dd", int(svc.Cert.NotAfter.Sub(now)/day)), "cert"})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.After(events[j].at) })
	if len(events) > 5 {
		events = events[:5]
	}
	return events
}

func truncateLines(text string, width int) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	return strings.Join(lines, "\n")
}

// splitGeometry is the split layout's list width, detail pane width and the
// height of the list and detail above the activity feed.
func splitGeometry(snap Snapshot, selected int, now time.Time, width, height int, filter listFilter) (leftWidth, rightWidth, topHeight int) {
	leftWidth = max(splitListWidth(snap, selected, now), filter.listWidth)
	return leftWidth, width - leftWidth - 3, max(0, height-2-(2+len(activity(snap, now)))-3)
}

// splitDetail is the detail pane of the selected service, fitted to the pane,
// without its key help, and the options used.
func splitDetail(snap Snapshot, selected int, now time.Time, loadErr error, width, height int, scroll incidentScroll, filter listFilter) ([]string, detailOptions) {
	if selected < 0 || selected >= len(snap.Services) {
		return nil, detailOptions{}
	}
	_, rightWidth, topHeight := splitGeometry(snap, selected, now, width, height, filter)
	svc := snap.Services[selected]
	opts := fullDetail()
	opts.compact, opts.cursor, opts.offset = true, scroll.cursor, scroll.offset
	// The pane leaves out the detail's key help.
	for {
		detail := renderDetailWithOptions(snap, svc, now, loadErr, rightWidth, opts)
		lines := strings.Split(strings.TrimRight(strings.TrimSuffix(detail, renderHelp(detailHelp(svc, opts))), "\n"), "\n")
		if len(lines) <= topHeight || !opts.shrink() {
			return lines, opts
		}
	}
}

func renderSplit(snap Snapshot, selected int, now time.Time, loadErr error, width, height int, notice string, scroll incidentScroll, filters ...listFilter) string {
	var filter listFilter
	if len(filters) > 0 {
		filter = filters[0]
	}
	leftWidth, rightWidth, topHeight := splitGeometry(snap, selected, now, width, height, filter)
	events := activity(snap, now)
	list := strings.TrimSuffix(renderList(snap, selected, now, loadErr, 1, filter), "\n"+footerFor(filter, 1))
	list = strings.TrimSuffix(list, "\n")
	if notice != "" {
		list += "\n" + notice
	}
	left := strings.Split(list, "\n")
	right, opts := splitDetail(snap, selected, now, loadErr, width, height, scroll, filter)
	var b strings.Builder
	for i := 0; i < topHeight; i++ {
		l, r := "", ""
		if i < len(left) {
			l = ansi.Truncate(left[i], leftWidth, "")
		}
		if i < len(right) {
			r = ansi.Truncate(right[i], rightWidth, "")
		}
		b.WriteString(l + strings.Repeat(" ", leftWidth-ansi.StringWidth(l)) + helpStyle.Render(" │ ") + r + "\n")
	}
	b.WriteString(helpStyle.Render(strings.Repeat("─", width)) + "\n")
	b.WriteString(headerStyle.Render("ACTIVITY") + "\n")
	for _, event := range events {
		glyph := downStyle.Render("●")
		if event.kind == "up" {
			glyph = upStyle.Render("●")
		} else if event.kind == "cert" {
			glyph = warnStyle.Render("!")
		}
		b.WriteString(event.at.In(now.Location()).Format("15:04") + "  " + glyph + " " + event.monitor + " " + event.text + "\n")
	}
	help := "↑/↓ select · n new · e edit · d delete · m mute · ? more · q quit"
	if selected >= 0 && selected < len(snap.Services) && opts.incidents && len(snap.Services[selected].Incidents) > opts.incidentRows {
		help = "↑/↓ select · pgup/dn incidents · n new · e edit · d delete · m mute · ? more · q quit"
	}
	footer := renderHelp(help)
	if filter.open {
		footer = filter.prompt(width)
	}
	b.WriteString("\n" + footer)
	return b.String()
}
