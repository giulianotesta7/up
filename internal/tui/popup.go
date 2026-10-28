package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const popupWidth = 46
const popupHeight = 14

func helpPopupFits(width, height int) bool {
	return (width <= 0 || width >= popupWidth+2) && (height <= 0 || height >= popupHeight+2)
}

// renderHelpPopup omits enter in split mode, where the detail is always shown
// and enter does nothing. The box keeps its height so it does not jump.
func renderHelpPopup(split bool) string {
	row := func(key, description, key2, description2 string) string {
		left := accentStyle.Render(key) + strings.Repeat(" ", 9-ansi.StringWidth(key)) + helpStyle.Render(description)
		if key2 == "" {
			return left
		}
		return left + strings.Repeat(" ", 14-ansi.StringWidth(description)) + accentStyle.Render(key2) + strings.Repeat(" ", 4-ansi.StringWidth(key2)) + helpStyle.Render(description2)
	}
	monitors := [][2]string{{"↑/↓ k/j", "select"}, {"enter", "details"}, {"n", "new monitor"}, {"e", "edit"}, {"d", "delete"}}
	alerting := [][2]string{{"m", "mute selected"}, {"u", "unmute"}, {"s", "SMTP servers"}, {"c", "channels"}}
	if split {
		monitors = append(monitors[:1:1], monitors[2:]...)
	}
	lines := []string{headerStyle.Render("MONITORS") + strings.Repeat(" ", 15) + headerStyle.Render("ALERTING")}
	for i := range 5 {
		var left, right [2]string
		if i < len(monitors) {
			left = monitors[i]
		}
		if i < len(alerting) {
			right = alerting[i]
		}
		lines = append(lines, strings.TrimRight(row(left[0], left[1], right[0], right[1]), " "))
	}
	lines = append(lines,
		"",
		headerStyle.Render("VIEW"),
		row("a", "animation on/off", "", ""),
		row("q", "quit", "", ""),
		"",
		accentStyle.Render("? or esc")+"  "+helpStyle.Render("close"),
	)
	border := func(s string) string { return accentStyle.Render(s) }
	out := []string{border("╭─ keys " + strings.Repeat("─", popupWidth-9) + "╮")}
	for _, line := range lines {
		out = append(out, border("│")+" "+line+strings.Repeat(" ", popupWidth-3-ansi.StringWidth(line))+border("│"))
	}
	out = append(out, border("╰"+strings.Repeat("─", popupWidth-2)+"╯"))
	return strings.Join(out, "\n")
}

// Leave the footer at the bottom of the available area, including when the
// terminal dimensions are not yet known and the content is short.
func padForPopup(content string, height int) string {
	if height <= 0 {
		height = max(popupHeight+2, strings.Count(content, "\n")+2)
	}
	return content + strings.Repeat("\n", max(0, height-2-strings.Count(content, "\n")))
}

// overlayPopup replaces cells, not bytes, and leaves the surrounding ANSI
// sequences intact. Short background lines are extended only to the box edge.
func overlayPopup(content, popup string, width, height int) string {
	lines := strings.Split(content, "\n")
	box := strings.Split(popup, "\n")
	boxWidth := 0
	for _, line := range box {
		boxWidth = max(boxWidth, ansi.StringWidth(line))
	}
	if width <= 0 {
		for _, line := range lines {
			width = max(width, ansi.StringWidth(line))
		}
	}
	if height <= 0 {
		height = len(lines)
	}
	left, top := max(0, (width-boxWidth)/2), max(0, (height-len(box))/2)
	for len(lines) < top+len(box) {
		lines = append(lines, "")
	}
	for i, boxLine := range box {
		line := lines[top+i]
		prefix := ansi.Truncate(line, left, "")
		prefix += strings.Repeat(" ", max(0, left-ansi.StringWidth(prefix)))
		right := ansi.TruncateLeft(line, left+boxWidth, "")
		lines[top+i] = prefix + boxLine + right
	}
	return strings.Join(lines, "\n")
}
