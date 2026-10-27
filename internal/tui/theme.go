package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	logoMark    = "▲"
	logoStem    = "└"
	productName = "up"
)

// All palette choices live here. Styles are immutable shared values; a future
// theme can replace this palette without changing the view's layout.
var theme = struct{ accent, dim, up, down, warn color.Color }{
	accent: lipgloss.Color("#a78bfa"),
	dim:    lipgloss.Color("#808080"),
	up:     lipgloss.Color("#22c55e"),
	down:   lipgloss.Color("#ef4444"),
	warn:   lipgloss.Color("#f59e0b"),
}

var (
	accentStyle       = lipgloss.NewStyle().Foreground(theme.accent)
	titleStyle        = accentStyle.Bold(true)
	selectedNameStyle = lipgloss.NewStyle().Bold(true)
	helpStyle         = lipgloss.NewStyle().Foreground(theme.dim)
	headerStyle       = helpStyle.Bold(true)
	upStyle           = lipgloss.NewStyle().Foreground(theme.up)
	downStyle         = lipgloss.NewStyle().Foreground(theme.down).Bold(true)
	slowStyle         = lipgloss.NewStyle().Foreground(theme.warn)
	pendingStyle      = helpStyle
	warnStyle         = lipgloss.NewStyle().Foreground(theme.warn).Bold(true)
)

func renderHelp(text string) string {
	keys := map[string]bool{"↑/↓": true, "(k/j)": true, "enter": true, "n": true, "e": true, "d": true, "a": true, "q": true, "ctrl+c": true, "esc": true, "tab/shift+tab": true, "y": true}
	words := strings.Split(text, " ")
	for i, word := range words {
		if keys[word] {
			words[i] = accentStyle.Render(word)
		} else {
			words[i] = helpStyle.Render(word)
		}
	}
	return strings.Join(words, " ")
}

func ruleWidth(width int) int {
	if width <= 0 {
		width = 60
	}
	return min(80, max(20, width))
}

// A negative position renders the original, byte-identical idle rule.
// The pulse replaces cells rather than adding them, clipping at the edge.
func renderRule(width, position int) string {
	length := ruleWidth(width) - 1
	stem := accentStyle.Render(logoStem)
	if position < 0 || position >= length {
		return stem + helpStyle.Render(strings.Repeat("─", length))
	}
	pulse := []rune("╱╲╱")[:min(3, length-position)]
	return stem + helpStyle.Render(strings.Repeat("─", position)) +
		accentStyle.Render(string(pulse)) + helpStyle.Render(strings.Repeat("─", length-position-len(pulse)))
}

func renderHeader(snap Snapshot, width int) string {
	return renderSweepHeader(snap, width, -1)
}

func renderSweepHeader(snap Snapshot, width, position int) string {
	return renderTitle(snap) + "\n" + renderRule(width, position) + "\n"
}
