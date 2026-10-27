package tui

import (
	"image/color"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

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
	keys := map[string]bool{"↑/↓": true, "(k/j)": true, "enter": true, "n": true, "e": true, "d": true, "s": true, "c": true, "a": true, "q": true, "ctrl+c": true, "esc": true, "tab/shift+tab": true, "←/→": true, "y": true}
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

// renderHostRule keeps every label cell intact while the pulse lights it.
// Positions are relative to the first cell after the stem, as in renderRule.
func renderHostRule(snap Snapshot, hostname string, now time.Time, width, position int) string {
	cells := ruleWidth(width)
	hostname = ansi.Truncate(ansi.Strip(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, hostname)), 24, "…")
	if snap.Stale(now) {
		position = -1
	}
	label := ""
	if hostname != "" {
		label = " " + hostname + " "
	}
	if 2+ansi.StringWidth(label)+3 > cells {
		label = ""
	}
	if label == "" {
		return renderRule(width, position)
	}
	var b strings.Builder
	b.WriteString(accentStyle.Render(logoStem))
	cell := 1
	write := func(text string, style lipgloss.Style, rule bool) {
		if position < 0 {
			b.WriteString(style.Render(text))
			cell += ansi.StringWidth(text)
			return
		}
		for _, r := range text {
			glyph := string(r)
			w := ansi.StringWidth(glyph)
			s := style
			if position >= 0 && cell < position+4 && cell+w > position+1 {
				s = accentStyle
				if rule {
					glyph = string([]rune("╱╲╱")[cell-position-1])
				}
			}
			b.WriteString(s.Render(glyph))
			cell += w
		}
	}
	write("─", helpStyle, true)
	write(label, helpStyle, false)
	write(strings.Repeat("─", max(0, cells-cell)), helpStyle, true)
	return b.String()
}

func renderHeader(snap Snapshot, width int) string {
	return renderSweepHeader(snap, width, -1)
}

func renderSweepHeader(snap Snapshot, width, position int) string {
	return renderTitle(snap) + "\n" + renderRule(width, position) + "\n"
}
