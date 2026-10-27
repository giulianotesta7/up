package tui

import "strings"

// cycleChoice wraps a selection in display order, preserving an unknown value
// until the user explicitly chooses an option.
func cycleChoice(options []string, value string, delta int) string {
	if len(options) == 0 {
		return ""
	}
	for i, option := range options {
		if option == value {
			return options[(i+delta+len(options))%len(options)]
		}
	}
	if delta < 0 {
		return options[len(options)-1]
	}
	return options[0]
}

func renderChoice(value string, options []string, focused bool) string {
	left, right := "‹", "›"
	if focused {
		left = accentStyle.Render(left)
		right = accentStyle.Render(right)
	}
	text := left + " " + value + " " + right
	if len(options) > 0 {
		text += "   " + helpStyle.Render(strings.Join(options, " · "))
	}
	return text
}
