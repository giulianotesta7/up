package tui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

const (
	minMute = time.Minute
	maxMute = 7 * 24 * time.Hour
)

// muteOptions are the picker rows; the last one asks for a duration.
var muteOptions = []struct {
	label    string
	duration time.Duration
}{
	{"30 minutes", 30 * time.Minute},
	{"1 hour", time.Hour},
	{"4 hours", 4 * time.Hour},
	{"24 hours", 24 * time.Hour},
	{"Custom…", 0},
}

type mutePrompt struct {
	name string
	// wasAlerting records whether the recipients were already told the
	// monitor is down when the mute begins.
	wasAlerting bool
	choice      int
	custom      bool
	input       string
	err         error
	saving      bool
}

type muteMsg struct {
	generation uint64
	err        error
}

type unmuteMsg struct {
	generation uint64
	err        error
}

func (p mutePrompt) duration() (time.Duration, error) {
	if !p.custom {
		return muteOptions[p.choice].duration, nil
	}
	d, err := time.ParseDuration(p.input)
	if err != nil {
		return 0, fmt.Errorf("duration %q is not a duration (use values like 45m or 2h)", p.input)
	}
	if d < minMute || d > maxMute {
		return 0, fmt.Errorf("duration must be between 1m and 7d")
	}
	return d, nil
}

func (m Model) handleMuteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := *m.mute
	m.mute = &p
	if p.saving {
		return m, nil
	}
	last := len(muteOptions) - 1
	switch key := msg.String(); {
	case key == "esc":
		if p.custom {
			p.custom, p.err = false, nil
		} else {
			m.mute = nil
		}
	case key == "enter":
		d, err := p.duration()
		if err == nil && !p.custom && p.choice == last {
			p.custom = true
			return m, nil
		}
		if p.err = err; err != nil {
			return m, nil
		}
		p.saving = true
		m.generation++
		until := m.now().Add(d)
		generation := m.generation
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			return muteMsg{generation: generation, err: m.store.SetMute(ctx, p.name, until, p.wasAlerting)}
		}
	case p.custom && key == "backspace":
		if r := []rune(p.input); len(r) > 0 {
			p.input = string(r[:len(r)-1])
		}
	case p.custom:
		if msg.Text != "" {
			p.input += strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return -1
				}
				return r
			}, msg.Text)
		}
	case key == "up" || key == "k":
		p.choice = max(0, p.choice-1)
	case key == "down" || key == "j":
		p.choice = min(last, p.choice+1)
	}
	return m, nil
}

// unmute expires the selected monitor's mute now.
func (m Model) unmute(name string) tea.Cmd {
	generation := m.generation
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		return unmuteMsg{generation: generation, err: m.store.Unmute(ctx, name, m.now())}
	}
}

func renderMute(p mutePrompt, snap Snapshot, now time.Time, loadErr error) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Mute alerts · "+p.name) + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))
	b.WriteString(helpStyle.Render("Checks keep running. Emails are held until the mute ends.") + "\n\n")
	if p.custom {
		fmt.Fprintf(&b, "%s %s: %s%s      %s\n", accentStyle.Render(">"), accentStyle.Render("Duration"), p.input, accentStyle.Render("│"), helpStyle.Render("e.g. 45m, 2h, 1h30m"))
	} else {
		for i, option := range muteOptions {
			if i == p.choice {
				fmt.Fprintf(&b, "  %s %s\n", accentStyle.Render(">"), accentStyle.Render(option.label))
			} else {
				fmt.Fprintf(&b, "    %s\n", option.label)
			}
		}
	}
	b.WriteString("\n" + helpStyle.Render(fmt.Sprintf("If %s is still down when the mute ends, you get the alert then.", p.name)) + "\n")
	if p.err != nil {
		b.WriteString("\n" + warnStyle.Render("Cannot mute: "+p.err.Error()) + "\n")
	}
	switch {
	case p.saving:
		b.WriteString("\n" + renderHelp("Muting… · ctrl+c quit"))
	case p.custom:
		b.WriteString("\n" + renderHelp("enter mute · esc back · ctrl+c quit"))
	default:
		b.WriteString("\n" + renderHelp("↑/↓ choose · enter mute · esc cancel · ctrl+c quit"))
	}
	return b.String()
}

// timeLeft renders a remaining mute as "45s", "28m" or "3h 12m".
func timeLeft(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(1, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		hours, minutes := int(d.Hours()), int(d.Minutes())%60
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours()/24), int(d.Hours())%24)
	}
}
