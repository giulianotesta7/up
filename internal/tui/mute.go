package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/store"
)

const (
	MinMute = time.Minute
	MaxMute = 7 * 24 * time.Hour
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
	// tags are the monitor's tags; target 0 mutes the monitor, target i
	// the tag tags[i-1] (with every monitor carrying it).
	tags   []string
	target int
	// wasAlerting records whether the recipients were already told the
	// monitor is down when the mute begins.
	wasAlerting bool
	choice      int
	custom      bool
	input       string
	err         error
	saving      bool
}

// tag is the tag the prompt mutes, or "" for the monitor.
func (p mutePrompt) tag() string {
	if p.target == 0 {
		return ""
	}
	return p.tags[p.target-1]
}

// targetName is what the prompt mutes: the monitor or "#tag".
func (p mutePrompt) targetName() string {
	if tag := p.tag(); tag != "" {
		return "#" + tag
	}
	return p.name
}

// TagMuter mutes and unmutes every monitor carrying a tag. Both the local
// *store.Store and the remote client implement it.
type TagMuter interface {
	SetTagMute(ctx context.Context, tag string, until time.Time) error
	UnmuteTag(ctx context.Context, tag string) error
}

var _ TagMuter = (*store.Store)(nil)

// errNoTagMute is shown when the store cannot mute tags.
var errNoTagMute = errors.New("this server cannot mute tags")

// unmuteTagPrompt asks before u ends a tag's mute, which also ends it for
// the tag's other monitors.
type unmuteTagPrompt struct {
	name, tag string
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
	if d < MinMute || d > MaxMute {
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
		tags, _ := m.store.(TagMuter)
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			var err error
			switch tag := p.tag(); {
			case tag == "":
				err = m.store.SetMute(ctx, p.name, until, p.wasAlerting)
			case tags == nil:
				err = errNoTagMute
			default:
				err = tags.SetTagMute(ctx, tag, until)
			}
			return muteMsg{generation: generation, err: err}
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
	case (key == "left" || key == "right") && len(p.tags) > 0:
		delta := 1
		if key == "left" {
			delta = -1
		}
		p.target = (p.target + delta + len(p.tags) + 1) % (len(p.tags) + 1)
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

// unmuteTag ends a tag's mute for all its monitors.
func (m Model) unmuteTag(tag string) tea.Cmd {
	generation := m.generation
	tags, _ := m.store.(TagMuter)
	return func() tea.Msg {
		if tags == nil {
			return unmuteMsg{generation: generation, err: errNoTagMute}
		}
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		return unmuteMsg{generation: generation, err: tags.UnmuteTag(ctx, tag)}
	}
}

func (m Model) handleUnmuteTagKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		tag := m.unmuteTagPrompt.tag
		m.unmuteTagPrompt = nil
		m.generation++
		return m, m.unmuteTag(tag)
	case "n", "esc":
		m.unmuteTagPrompt = nil
	}
	return m, nil
}

func renderUnmuteTag(p unmuteTagPrompt, snap Snapshot, now time.Time, loadErr error) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Unmute #"+p.tag) + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))
	b.WriteString(p.name + " is muted through #" + p.tag + ".\n")
	prefix := "Unmuting ends the mute for "
	b.WriteString(prefix + membersText(snap.tagMembers(p.tag), p.tag, popupRowWidth-ansi.StringWidth(prefix)-1) + ".\n")
	b.WriteString("\n" + renderHelp("y unmute · n cancel"))
	return b.String()
}

func renderMute(p mutePrompt, snap Snapshot, now time.Time, loadErr error) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Mute alerts · "+p.targetName()) + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))
	b.WriteString(helpStyle.Render("Checks keep running. Alerts are held until the mute ends.") + "\n\n")
	if len(p.tags) > 0 {
		row := "Mute " + renderChoice(p.targetName(), nil, !p.custom)
		if tag := p.tag(); tag != "" {
			row += "  " + helpStyle.Render(coversText(snap, tag, popupRowWidth-ansi.StringWidth(row)-2))
		}
		b.WriteString(row + "\n\n")
	}
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
	who := p.name
	if p.tag() != "" {
		who = "a monitor"
	}
	b.WriteString("\n" + helpStyle.Render(fmt.Sprintf("If %s is still down when the mute ends, you get the alert then.", who)) + "\n")
	if p.err != nil {
		b.WriteString("\n" + warnStyle.Render("Cannot mute: "+p.err.Error()) + "\n")
	}
	switch {
	case p.saving:
		b.WriteString("\n" + renderHelp("Muting… · ctrl+c quit"))
	case p.custom:
		b.WriteString("\n" + renderHelp("enter mute · esc back · ctrl+c quit"))
	case len(p.tags) > 0:
		b.WriteString("\n" + renderHelp("←/→ target · ↑/↓ choose · enter mute · esc cancel · ctrl+c quit"))
	default:
		b.WriteString("\n" + renderHelp("↑/↓ choose · enter mute · esc cancel · ctrl+c quit"))
	}
	return b.String()
}

// outageDuration renders how long an outage lasted as "45s", "1m 33s",
// "28m", "3h 12m" or "2d 2h": seconds matter for short outages only.
func outageDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	}
	if d < time.Hour {
		minutes, seconds := int(d.Minutes()), int(d.Seconds())%60
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	}
	return timeLeft(d)
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
