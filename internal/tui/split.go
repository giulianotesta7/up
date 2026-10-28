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
		if strings.Contains(ansi.Strip(line), "MONITOR") && strings.Contains(ansi.Strip(line), "LAST CHECK") {
			return ansi.StringWidth(line)
		}
	}
	return ansi.StringWidth("No checks configured.")
}

func (m Model) splitMode() bool {
	return m.width >= 110 && m.height >= 30 && m.form == nil && m.deletion == nil && m.mute == nil && !m.help && !m.smtpScreen && !m.channelScreen && m.width >= splitListWidth(m.snap, m.selected, m.now())+3+minDetailWidth
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
				events = append(events, activityEvent{o.End, svc.Check.Name, "recovered after " + o.End.Sub(o.Start).String(), "up"})
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

func renderSplit(snap Snapshot, selected int, now time.Time, loadErr error, width, height int, notice string) string {
	leftWidth := splitListWidth(snap, selected, now)
	rightWidth := width - leftWidth - 3
	events := activity(snap, now)
	topHeight := max(0, height-2-(2+len(events))-3)
	list := strings.TrimSuffix(renderList(snap, selected, now, loadErr, 1), "\n"+renderHelp("↑/↓ select · enter open · n new · e edit · d delete · m mute · ? more · q quit"))
	list = strings.TrimSuffix(list, "\n")
	if notice != "" {
		list += "\n" + warnStyle.Render(notice)
	}
	left := strings.Split(list, "\n")
	var right []string
	if selected >= 0 && selected < len(snap.Services) {
		opts := detailOptions{compact: true, config: true, cert: true, outages: true, chartRows: 5}
		for {
			detail := renderDetailWithOptions(snap, snap.Services[selected], now, loadErr, rightWidth, opts)
			detail = strings.TrimSuffix(detail, "\n"+renderHelp("esc back · e edit · d delete · m mute · ? more · q quit"))
			right = strings.Split(strings.TrimSuffix(detail, "\n"), "\n")
			if len(right) <= topHeight {
				break
			}
			switch {
			case opts.config:
				opts.config = false
			case opts.cert:
				opts.cert = false
			case opts.outages:
				opts.outages = false
			case opts.chartRows > 2:
				opts.chartRows--
			default:
				goto fitted
			}
		}
	}

fitted:
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
	b.WriteString(headerStyle.Render("ACTIVITY") + " " + helpStyle.Render("all monitors, newest first") + "\n")
	for _, event := range events {
		glyph := downStyle.Render("●")
		if event.kind == "up" {
			glyph = upStyle.Render("●")
		} else if event.kind == "cert" {
			glyph = warnStyle.Render("!")
		}
		b.WriteString(event.at.In(now.Location()).Format("15:04") + "  " + glyph + " " + event.monitor + " " + event.text + "\n")
	}
	b.WriteString("\n" + renderHelp("↑/↓ select · n new · e edit · d delete · m mute · ? more · q quit"))
	return b.String()
}
