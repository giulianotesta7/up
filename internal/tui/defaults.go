package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/heohlee/up/internal/config"
)

// Rows of the Defaults popup, in tab order. The text rows (zone, interval,
// timeout, status) keep their draft in defaultsPopup.values.
const (
	defaultsZone = iota
	defaultsChannel
	defaultsInterval
	defaultsTimeout
	defaultsStatus
	defaultsAlertAfter
	defaultsRecoverAfter
	defaultsRows
)

// defaultsBaseWidth is the least width of the popup, so it does not change as the focus moves.
const defaultsBaseWidth = 64

// errNoDefaults is shown when the store cannot save defaults.
var errNoDefaults = errors.New("this server does not support defaults")

// defaultsPopup edits the defaults new monitors and maintenance windows
// start from. Saving never touches existing ones.
type defaultsPopup struct {
	values       [4]string // zone, interval, timeout, status
	channel      string    // "" is none
	alertAfter   string
	recoverAfter string
	focus        int
	cursor       int // rune offset in the focused text row
	err          error
	saving       bool
}

type defaultsMsg struct {
	generation uint64
	defaults   config.Defaults
	err        error
}

func newDefaultsPopup(snap Snapshot) *defaultsPopup {
	d := snap.NewItemDefaults()
	p := &defaultsPopup{
		values:  [4]string{d.Zone, shortDuration(d.Interval), shortDuration(d.Timeout), strconv.Itoa(d.ExpectedStatus)},
		channel: snap.existingChannel(d.Channel), alertAfter: strconv.Itoa(d.AlertAfter), recoverAfter: strconv.Itoa(d.RecoverAfter),
	}
	p.cursor = len([]rune(p.values[0]))
	return p
}

// textIndex maps a focused row to its draft in values, or -1 for a choice.
func textIndex(focus int) int {
	switch focus {
	case defaultsZone:
		return 0
	case defaultsInterval, defaultsTimeout, defaultsStatus:
		return focus - defaultsInterval + 1
	}
	return -1
}

// defaults converts text only; the rules belong to config.ValidateDefaults.
func (p defaultsPopup) defaults() (config.Defaults, error) {
	d := config.Defaults{Zone: strings.TrimSpace(p.values[0]), Channel: p.channel}
	var err error
	if d.Interval, err = time.ParseDuration(strings.TrimSpace(p.values[1])); err != nil {
		return d, fmt.Errorf("interval: %w", err)
	}
	if d.Timeout, err = time.ParseDuration(strings.TrimSpace(p.values[2])); err != nil {
		return d, fmt.Errorf("timeout: %w", err)
	}
	if d.ExpectedStatus, err = strconv.Atoi(strings.TrimSpace(p.values[3])); err != nil {
		return d, fmt.Errorf("expected status: %w", err)
	}
	if d.AlertAfter, err = strconv.Atoi(p.alertAfter); err != nil {
		return d, fmt.Errorf("alert after: %w", err)
	}
	if d.RecoverAfter, err = strconv.Atoi(p.recoverAfter); err != nil {
		return d, fmt.Errorf("recover after: %w", err)
	}
	return d, config.ValidateDefaults(d)
}

func (p *defaultsPopup) setFocus(to int) {
	p.focus = (to + defaultsRows) % defaultsRows
	p.cursor = 0
	if i := textIndex(p.focus); i >= 0 {
		p.cursor = len([]rune(p.values[i]))
	}
}

func (p *defaultsPopup) insert(text string) {
	i := textIndex(p.focus)
	if i < 0 {
		return
	}
	editor := monitorForm{cursor: p.cursor}
	editor.values[0] = p.values[i]
	editor.insert(text)
	p.values[i], p.cursor = editor.values[0], editor.cursor
}

func (p *defaultsPopup) edit(msg tea.KeyPressMsg) {
	i := textIndex(p.focus)
	if i < 0 {
		return
	}
	editor := monitorForm{cursor: p.cursor}
	editor.values[0] = p.values[i]
	editor.edit(msg)
	p.values[i], p.cursor = editor.values[0], editor.cursor
}

func (m Model) handleDefaultsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := *m.defaults
	m.defaults = &p
	if p.saving {
		return m, nil
	}
	switch msg.String() {
	case "esc":
		m.defaults = nil
	case "tab":
		p.setFocus(p.focus + 1)
	case "shift+tab":
		p.setFocus(p.focus - 1)
	case "enter":
		d, err := p.defaults()
		if p.err = err; err != nil {
			return m, nil
		}
		return m.saveDefaults(&p, d)
	default:
		if textIndex(p.focus) >= 0 {
			p.edit(msg)
			return m, nil
		}
		delta := 0
		switch msg.String() {
		case "left":
			delta = -1
		case "right":
			delta = 1
		}
		switch {
		case delta == 0:
		case p.focus == defaultsChannel:
			p.channel = cycleChoice(channelChoices(m.snap), p.channel, delta)
		case p.focus == defaultsAlertAfter:
			p.alertAfter = cycleChoice(thresholdChoices, p.alertAfter, delta)
		default:
			p.recoverAfter = cycleChoice(thresholdChoices, p.recoverAfter, delta)
		}
	}
	return m, nil
}

func (m Model) handleDefaultsPaste(content string) Model {
	if m.defaults == nil || m.defaults.saving {
		return m
	}
	p := *m.defaults
	p.insert(content)
	m.defaults = &p
	return m
}

// saveDefaults runs the write; defaultsMsg closes the popup on success and
// shows the store's error otherwise.
func (m Model) saveDefaults(p *defaultsPopup, d config.Defaults) (tea.Model, tea.Cmd) {
	p.saving, p.err = true, nil
	// Invalidate the pre-save read/tick chain before any database write.
	m.generation++
	generation := m.generation
	writer, _ := m.store.(DefaultsStore)
	return m, func() tea.Msg {
		if writer == nil {
			return defaultsMsg{generation: generation, err: errNoDefaults}
		}
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		return defaultsMsg{generation: generation, defaults: d, err: writer.SetDefaults(ctx, d)}
	}
}

func (m Model) handleDefaultsMsg(msg defaultsMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.generation || m.defaults == nil || !m.defaults.saving {
		return m, nil
	}
	p := *m.defaults
	m.defaults = &p
	p.saving = false
	if msg.err != nil {
		p.err = msg.err
		return m, nil
	}
	m.defaults = nil
	m.snap.Defaults = msg.defaults
	m.notice, m.noticeOK = "✓ Defaults saved", true
	return m, m.load
}

// noticeLine is the one-line message under the list: a confirmation in green,
// anything else in amber.
func (m Model) noticeLine() string {
	if m.notice == "" {
		return ""
	}
	if m.noticeOK {
		return upStyle.Render(m.notice)
	}
	return warnStyle.Render(m.notice)
}

// defaultsMinWidth is the widest the popup gets over every focus.
func defaultsMinWidth(p defaultsPopup, snap Snapshot, now time.Time, loadErr error) int {
	minimum := defaultsBaseWidth
	for focus := range defaultsRows {
		p.setFocus(focus)
		minimum = max(minimum, modalContentWidth(renderDefaults(p, snap, now, loadErr)))
	}
	return minimum
}

func renderDefaults(p defaultsPopup, snap Snapshot, now time.Time, loadErr error) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Defaults") + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))
	b.WriteString(helpStyle.Render("New items only, except where noted.") + "\n\n")
	row := func(focus int, label, rendered string) {
		marker := " "
		if p.focus == focus {
			marker, label = accentStyle.Render(">"), accentStyle.Render(label)
		}
		fmt.Fprintf(&b, "%s %s: %s\n", marker, label, rendered)
	}
	text := func(focus int, label, hint string) {
		value := p.values[textIndex(focus)]
		if p.focus == focus {
			r := []rune(value)
			cursor := min(p.cursor, len(r))
			value = string(r[:cursor]) + accentStyle.Render("│") + string(r[cursor:]) + "   " + helpStyle.Render(hint)
		}
		row(focus, label, value)
	}
	b.WriteString(headerStyle.Render("NEW MAINTENANCE WINDOWS") + "\n")
	text(defaultsZone, "IANA zone", "e.g. America/New_York")
	b.WriteString("  " + helpStyle.Render("Also the time in heartbeat reasons and when cron heartbeats run.") + "\n")
	b.WriteString("\n" + headerStyle.Render("NEW MONITORS") + "\n")
	choices := channelChoices(snap)
	choices[0] = "none"
	selected := cmpNone(p.channel)
	rendered := renderChoice(selected, choices, p.focus == defaultsChannel)
	if len(snap.Channels) == 0 {
		rendered += "   " + helpStyle.Render("Add a channel with c on the list")
	}
	row(defaultsChannel, "Channel", rendered)
	text(defaultsInterval, "Interval", "e.g. 30s, 1m")
	text(defaultsTimeout, "Timeout", "e.g. 5s, 10s")
	text(defaultsStatus, "Expected status", "100–599")
	row(defaultsAlertAfter, "Alert after", renderChoiceUnit(p.alertAfter, "failures in a row", thresholdChoices, p.focus == defaultsAlertAfter))
	row(defaultsRecoverAfter, "Recover after", renderChoiceUnit(p.recoverAfter, "successes in a row", thresholdChoices, p.focus == defaultsRecoverAfter))
	if hint := detectionHint(monitorForm{alertAfter: p.alertAfter, values: [5]string{2: strings.TrimSpace(p.values[1])}}); hint != "" {
		b.WriteString("\n" + helpStyle.Render(hint) + "\n")
	}
	if p.err != nil {
		b.WriteString("\n" + warnStyle.Render("Cannot save defaults: "+p.err.Error()) + "\n")
	}
	switch {
	case p.saving:
		b.WriteString("\n" + renderHelp("Saving defaults… · ctrl+c quit"))
	case textIndex(p.focus) < 0:
		b.WriteString("\n" + renderHelp("←/→ choose · tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	default:
		b.WriteString("\n" + renderHelp("tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	}
	return b.String()
}

// cmpNone shows an empty channel as "none".
func cmpNone(channel string) string {
	if channel == "" {
		return "none"
	}
	return channel
}
