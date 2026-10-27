package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/giulianotesta7/up/internal/config"
)

var fieldLabels = [...]string{"Name", "HTTP/HTTPS URL", "Interval", "Timeout", "Expected status"}

type monitorForm struct {
	values       [5]string
	channel      string
	focus        int
	cursor       int // rune offset in the focused field
	err          error
	saving       bool
	editing      bool
	returnDetail bool
}

func newMonitorForm() *monitorForm {
	return &monitorForm{values: [5]string{"", "", "30s", "5s", "200"}}
}

func editMonitorForm(c config.Check, detail bool) *monitorForm {
	return &monitorForm{
		values:  [5]string{c.Name, c.URL, c.Interval.String(), c.Timeout.String(), strconv.Itoa(c.ExpectedStatus)},
		channel: c.Channel,
		focus:   1, cursor: len([]rune(c.URL)), editing: true, returnDetail: detail,
	}
}

// check converts text only; all semantic rules belong to config.ValidateCheck.
func (f monitorForm) check() (config.Check, error) {
	c := config.Check{Name: f.values[0], URL: f.values[1], Channel: f.channel}
	var err error
	if c.Interval, err = time.ParseDuration(f.values[2]); err != nil {
		return c, fmt.Errorf("interval: %w", err)
	}
	if c.Timeout, err = time.ParseDuration(f.values[3]); err != nil {
		return c, fmt.Errorf("timeout: %w", err)
	}
	if c.ExpectedStatus, err = strconv.Atoi(f.values[4]); err != nil {
		return c, fmt.Errorf("expected status: %w", err)
	}
	return c, config.ValidateCheck(c)
}

type saveMsg struct {
	generation uint64
	name       string
	err        error
}

func (m Model) save(c config.Check) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		var err error
		if m.form.editing {
			err = m.store.UpdateCheck(ctx, c)
		} else {
			err = m.store.AddCheck(ctx, c)
		}
		return saveMsg{generation: m.generation, name: c.Name, err: err}
	}
}

func (m Model) handleFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Copy the pointed-to form so Update never mutates an older model.
	f := *m.form
	m.form = &f
	if f.saving {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.form = nil
		return m, nil
	case "tab", "shift+tab":
		delta := 1
		if msg.String() == "shift+tab" {
			delta = -1
		}
		first := 0
		if f.editing {
			first = 1 // The stored name is displayed, never focused or editable.
		}
		count := len(f.values) + 1 - first
		f.focus = first + (f.focus-first+delta+count)%count
		if f.focus < len(f.values) {
			f.cursor = len([]rune(f.values[f.focus]))
		} else {
			f.cursor = 0
		}
	case "enter":
		c, err := f.check()
		f.err = err
		if err != nil {
			return m, nil
		}
		f.saving = true
		// Invalidate the pre-save read/tick chain before any database write.
		m.generation++
		return m, m.save(c)
	default:
		if f.focus == len(f.values) {
			delta := 0
			switch msg.String() {
			case "left":
				delta = -1
			case "right":
				delta = 1
			}
			if delta != 0 {
				f.channel = cycleChoice(channelChoices(m.snap), f.channel, delta)
			}
		} else {
			f.edit(msg)
		}
	}
	return m, nil
}

func channelChoices(snap Snapshot) []string {
	choices := make([]string, 0, len(snap.Channels)+1)
	for _, c := range snap.Channels {
		choices = append(choices, c.Name)
	}
	sort.Strings(choices)
	return append([]string{""}, choices...)
}

func (f *monitorForm) insert(text string) {
	if f.focus >= len(f.values) {
		return
	}
	// Paste is single-line too. Ignore terminal controls, not Unicode text.
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	value := []rune(f.values[f.focus])
	inserted := []rune(text)
	value = append(value[:f.cursor], append(inserted, value[f.cursor:]...)...)
	f.cursor += len(inserted)
	f.values[f.focus] = string(value)
}

func (f *monitorForm) edit(msg tea.KeyPressMsg) {
	value := []rune(f.values[f.focus])
	switch msg.String() {
	case "left":
		f.cursor = max(0, f.cursor-1)
	case "right":
		f.cursor = min(len(value), f.cursor+1)
	case "home":
		f.cursor = 0
	case "end":
		f.cursor = len(value)
	case "backspace":
		if f.cursor > 0 {
			value = append(value[:f.cursor-1], value[f.cursor:]...)
			f.cursor--
			f.values[f.focus] = string(value)
		}
	case "delete":
		if f.cursor < len(value) {
			f.values[f.focus] = string(append(value[:f.cursor], value[f.cursor+1:]...))
		}
	default:
		if msg.Text != "" {
			f.insert(msg.Text)
		}
	}
}

func renderForm(f monitorForm, snap Snapshot, now time.Time, loadErr error) string {
	var b strings.Builder
	title := "New monitor"
	if f.editing {
		title = "Edit monitor"
	}
	b.WriteString(titleStyle.Render(title) + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))
	for i, label := range fieldLabels {
		marker := " "
		value := f.values[i]
		if i == f.focus {
			marker = ">"
			runes := []rune(value)
			value = string(runes[:f.cursor]) + accentStyle.Render("│") + string(runes[f.cursor:])
			label = accentStyle.Render(label)
			marker = accentStyle.Render(marker)
		}
		fmt.Fprintf(&b, "%s %s: %s\n", marker, label, value)
	}
	choices := channelChoices(snap)
	choices[0] = "none"
	selected := f.channel
	if selected == "" {
		selected = "none"
	}
	focused := f.focus == len(f.values)
	marker, label := " ", "Channel"
	if focused {
		marker, label = accentStyle.Render(">"), accentStyle.Render(label)
	}
	fmt.Fprintf(&b, "%s %s: %s\n", marker, label, renderChoice(selected, choices, focused))
	if f.err != nil {
		b.WriteString("\n" + warnStyle.Render("Cannot save monitor: "+f.err.Error()) + "\n")
	}
	if f.saving {
		b.WriteString("\n" + renderHelp("Saving monitor… · ctrl+c quit"))
	} else if focused {
		b.WriteString("\n" + renderHelp("←/→ choose · tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	} else {
		b.WriteString("\n" + renderHelp("tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	}
	return b.String()
}
