package tui

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
	return renderChoiceUnit(value, "", options, focused)
}

// renderChoiceUnit is renderChoice with a unit after the selected value, as
// in "‹ 3 › failures in a row".
func renderChoiceUnit(value, unit string, options []string, focused bool) string {
	left, right := "‹", "›"
	if focused {
		left = accentStyle.Render(left)
		right = accentStyle.Render(right)
	}
	text := left + " " + value + " " + right
	if unit != "" {
		text += " " + unit
	}
	return text
}
