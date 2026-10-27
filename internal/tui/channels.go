package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

var channelLabels = [...]string{"Name", "Kind", "SMTP server", "Recipients"}

type channelForm struct {
	values          [4]string
	focus, cursor   int
	editing, saving bool
	err             error
}
type channelSaveMsg struct {
	generation uint64
	name       string
	err        error
}
type channelDeleteMsg struct {
	generation uint64
	err        error
}

func smtpNames(snap Snapshot) []string {
	names := make([]string, 0, len(snap.SMTPServers))
	for _, s := range snap.SMTPServers {
		names = append(names, s.Name)
	}
	return names
}
func newChannelForm(snap Snapshot) *channelForm {
	f := &channelForm{values: [4]string{"", config.KindEmail, "", ""}}
	if names := smtpNames(snap); len(names) > 0 {
		f.values[2] = names[0]
	}
	return f
}
func editChannelForm(c config.Channel) *channelForm {
	return &channelForm{values: [4]string{c.Name, c.Kind, c.SMTPServer, strings.Join(c.Recipients, ", ")}, focus: 1, editing: true}
}
func (f *channelForm) insert(text string) {
	if f.focus == 1 || f.focus == 2 {
		return
	}
	editor := monitorForm{focus: 0, cursor: f.cursor}
	editor.values[0] = f.values[f.focus]
	editor.insert(text)
	f.values[f.focus] = editor.values[0]
	f.cursor = editor.cursor
}
func (f *channelForm) edit(msg tea.KeyPressMsg, snap Snapshot) {
	if f.focus == 1 || f.focus == 2 {
		options := config.ChannelKinds
		if f.focus == 2 {
			options = smtpNames(snap)
		}
		switch msg.String() {
		case "left":
			f.values[f.focus] = cycleChoice(options, f.values[f.focus], -1)
		case "right":
			f.values[f.focus] = cycleChoice(options, f.values[f.focus], 1)
		}
		return
	}
	editor := monitorForm{focus: 0, cursor: f.cursor}
	editor.values[0] = f.values[f.focus]
	editor.edit(msg)
	f.values[f.focus] = editor.values[0]
	f.cursor = editor.cursor
}
func (m Model) handleChannelFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := *m.channelForm
	m.channelForm = &f
	if f.saving {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.channelForm = nil
	case "tab", "shift+tab":
		delta := 1
		if msg.String() == "shift+tab" {
			delta = -1
		}
		first := 0
		if f.editing {
			first = 1
		}
		count := len(f.values) - first
		f.focus = first + (f.focus-first+delta+count)%count
		f.cursor = len([]rune(f.values[f.focus]))
	case "enter":
		channel := config.Channel{Name: f.values[0], Kind: f.values[1], SMTPServer: f.values[2], Recipients: config.ParseRecipients(f.values[3])}
		editing := f.editing
		f.saving = true
		m.generation++
		generation := m.generation
		return m, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			var err error
			if editing {
				err = m.store.UpdateChannel(ctx, channel)
			} else {
				err = m.store.AddChannel(ctx, channel)
			}
			return channelSaveMsg{generation: generation, name: channel.Name, err: err}
		}
	default:
		f.edit(msg, m.snap)
	}
	return m, nil
}
func (m Model) handleChannelKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.channelForm != nil {
		return m.handleChannelFormKey(msg)
	}
	if m.channelDelete != nil {
		p := *m.channelDelete
		m.channelDelete = &p
		if p.saving {
			return m, nil
		}
		switch msg.String() {
		case "n", "esc":
			m.channelDelete = nil
		case "y":
			p.saving = true
			m.generation++
			generation := m.generation
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
				defer cancel()
				return channelDeleteMsg{generation: generation, err: m.store.DeleteChannel(ctx, p.name)}
			}
		}
		return m, nil
	}
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "esc":
		m.channelScreen = false
		m.detail = false
	case "up", "k":
		if m.channelSelected > 0 {
			m.channelSelected--
		}
	case "down", "j":
		if m.channelSelected < len(m.snap.Channels)-1 {
			m.channelSelected++
		}
	case "n":
		m.channelForm = newChannelForm(m.snap)
	case "e", "d":
		if m.channelSelected < len(m.snap.Channels) {
			c := m.snap.Channels[m.channelSelected]
			if msg.String() == "e" {
				m.channelForm = editChannelForm(c)
			} else {
				m.channelDelete = &deletePrompt{name: c.Name}
			}
		}
	}
	return m, nil
}
func renderChannelList(snap Snapshot, selected int, now time.Time, loadErr error) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Channels") + "\n" + renderBanners(snap, now, loadErr))
	if len(snap.Channels) == 0 {
		b.WriteString(helpStyle.Render("No channels configured.") + "\n")
	} else {
		widths := [4]int{4, 4, len("SMTP SERVER"), len("RECIPIENTS")}
		rows := make([][4]string, len(snap.Channels))
		counts := make([]int, len(rows))
		countWidth := len("MONITORS")
		for i, c := range snap.Channels {
			rows[i] = [4]string{c.Name, c.Kind, c.SMTPServer, strings.Join(c.Recipients, ", ")}
			for j, v := range rows[i] {
				widths[j] = max(widths[j], ansi.StringWidth(v))
			}
			for _, s := range snap.Services {
				if s.Check.Channel == c.Name {
					counts[i]++
				}
			}
			countWidth = max(countWidth, len(strconv.Itoa(counts[i])))
		}
		line := func(marker string, cols [4]string, last string) string {
			var out strings.Builder
			out.WriteString(marker + "   ")
			for i, v := range cols {
				out.WriteString(v)
				out.WriteString(strings.Repeat(" ", widths[i]-ansi.StringWidth(v)+2))
			}
			out.WriteString(strings.Repeat(" ", countWidth-ansi.StringWidth(last)) + last)
			return out.String()
		}
		b.WriteString(headerStyle.Render(line(" ", [4]string{"NAME", "KIND", "SMTP SERVER", "RECIPIENTS"}, "MONITORS")) + "\n")
		for i, row := range rows {
			marker := " "
			if i == selected {
				marker = accentStyle.Render("▌")
				row[0] = selectedNameStyle.Render(row[0])
			}
			b.WriteString(line(marker, row, strconv.Itoa(counts[i])) + "\n")
		}
	}
	b.WriteString("\n" + renderHelp("↑/↓ select (k/j) · n new · e edit · d delete · esc back · q quit · ctrl+c quit"))
	return b.String()
}
func renderChannelForm(f channelForm, snap Snapshot, now time.Time, loadErr error) string {
	var b strings.Builder
	title := "New channel"
	if f.editing {
		title = "Edit channel"
	}
	b.WriteString(titleStyle.Render(title) + "\n" + renderBanners(snap, now, loadErr))
	for i, label := range channelLabels {
		marker := " "
		value := f.values[i]
		focused := i == f.focus
		if i == 1 {
			value = renderChoice(value, nil, focused)
		} else if i == 2 {
			names := smtpNames(snap)
			if len(names) == 0 {
				value = helpStyle.Render("(none — press esc, then s to add one)")
			} else {
				value = renderChoice(value, names, focused)
			}
		} else if focused {
			runes := []rune(value)
			value = string(runes[:f.cursor]) + accentStyle.Render("│") + string(runes[f.cursor:])
		}
		if focused {
			marker = accentStyle.Render(">")
			label = accentStyle.Render(label)
		}
		fmt.Fprintf(&b, "%s %s: %s\n", marker, label, value)
	}
	if f.err != nil {
		b.WriteString("\n" + warnStyle.Render("Cannot save channel: "+f.err.Error()) + "\n")
	}
	b.WriteString("\n" + helpStyle.Render("recipients separated by commas") + "\n")
	if f.saving {
		b.WriteString(renderHelp("Saving channel… · ctrl+c quit"))
	} else if f.focus == 1 || f.focus == 2 {
		b.WriteString(renderHelp("←/→ choose · tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	} else {
		b.WriteString(renderHelp("tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	}
	return b.String()
}
func renderChannelDelete(p deletePrompt, snap Snapshot, now time.Time, loadErr error) string {
	content := titleStyle.Render("Delete channel") + "\n" + renderBanners(snap, now, loadErr) + fmt.Sprintf("Delete channel %q? This cannot be undone.\n", p.name)
	if p.err != nil {
		var in *store.InUseError
		message := "Cannot delete channel: " + p.err.Error()
		if errors.As(p.err, &in) {
			message = fmt.Sprintf("Cannot delete channel %q: %s", p.name, in.Reason())
		}
		content += "\n" + warnStyle.Render(message) + "\n"
	}
	if p.saving {
		content += "\n" + renderHelp("Deleting channel… · ctrl+c quit")
	} else {
		content += "\n" + renderHelp("y delete · n cancel")
	}
	return content
}
