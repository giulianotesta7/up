package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// channelRow is one row of the channel form. Which rows show depends on the
// kind (kindRows); channelForm.focus indexes them.
type channelRow int

const (
	rowName channelRow = iota
	rowKind
	rowSMTP
	rowRecipients
	rowURL
	rowAuthName
	rowAuth
	rowServer
	rowTopic
	rowToken
	rowChat
	rowPriority
)

// kindRows are the rows a kind shows, top to bottom.
func kindRows(kind string) []channelRow {
	rows := []channelRow{rowName, rowKind}
	switch kind {
	case config.KindWebhook:
		return append(rows, rowURL, rowAuthName, rowAuth)
	case config.KindNtfy:
		return append(rows, rowServer, rowTopic, rowToken, rowPriority)
	case config.KindTelegram:
		return append(rows, rowToken, rowChat)
	case config.KindDiscord, config.KindSlack:
		return append(rows, rowURL)
	case config.KindGotify:
		return append(rows, rowServer, rowToken, rowPriority)
	}
	return append(rows, rowSMTP, rowRecipients)
}

// maxKindRows is the most rows any kind shows. Shorter kinds pad to it, so
// the box keeps one size while Kind cycles.
func maxKindRows() int {
	n := 0
	for _, kind := range config.ChannelKinds {
		n = max(n, len(kindRows(kind)))
	}
	return n
}

type channelForm struct {
	values          [4]string // name, kind, SMTP server, recipients
	focus, cursor   int
	editing, saving bool
	err             error
	hook            hookForm
	push            pushForm
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
	f := &channelForm{values: [4]string{"", config.KindEmail, "", ""}, push: newPushForm()}
	if names := smtpNames(snap); len(names) > 0 {
		f.values[2] = names[0]
	}
	return f
}
func editChannelForm(c config.Channel) *channelForm {
	return &channelForm{values: [4]string{c.Name, c.Kind, c.SMTPServer, strings.Join(c.Recipients, ", ")}, focus: 1, editing: true, hook: editHookForm(c), push: editPushForm(c)}
}

func (f channelForm) kind() string { return f.values[1] }

func (f channelForm) rows() []channelRow { return kindRows(f.kind()) }

// fieldCount is how many rows tab cycles through.
func (f channelForm) fieldCount() int { return len(f.rows()) }

// row is the focused row.
func (f channelForm) row() channelRow {
	rows := f.rows()
	if f.focus < 0 || f.focus >= len(rows) {
		return rowName
	}
	return rows[f.focus]
}

func (f channelForm) label(r channelRow) string {
	switch r {
	case rowKind:
		return "Kind"
	case rowSMTP:
		return "SMTP server"
	case rowRecipients:
		return "Recipients"
	case rowURL:
		if f.kind() == config.KindWebhook {
			return "URL"
		}
		return "Webhook URL"
	case rowAuthName:
		return "Auth header name"
	case rowAuth:
		return "Auth value"
	case rowServer:
		return "Server URL"
	case rowTopic:
		return "Topic"
	case rowToken:
		return tokenLabel(f.kind())
	case rowChat:
		return "Chat ID"
	case rowPriority:
		return "Priority"
	}
	return "Name"
}

// text is the typed text of a row; nil for a choice.
func (f *channelForm) text(r channelRow) *string {
	switch r {
	case rowName:
		return &f.values[0]
	case rowRecipients:
		return &f.values[3]
	case rowURL:
		return &f.hook.url.text
	case rowAuthName:
		return &f.hook.authName
	case rowAuth:
		return &f.hook.auth.text
	case rowServer:
		return &f.push.server[pushSlot(f.kind())]
	case rowTopic:
		return &f.push.topic
	case rowToken:
		return &f.push.token.text
	case rowChat:
		return &f.push.chat
	}
	return nil
}

// choice is the selected value of a ‹ › row; nil for typed text.
func (f *channelForm) choice(r channelRow) *string {
	switch r {
	case rowKind:
		return &f.values[1]
	case rowSMTP:
		return &f.values[2]
	case rowPriority:
		return &f.push.priority[pushSlot(f.kind())]
	}
	return nil
}

func (f channelForm) choices(r channelRow, snap Snapshot) []string {
	switch r {
	case rowSMTP:
		return smtpNames(snap)
	case rowPriority:
		return priorities(f.kind())
	}
	return config.ChannelKinds
}

// fieldText is the text of the focused row.
func (f *channelForm) fieldText() string {
	if t := f.text(f.row()); t != nil {
		return *t
	}
	return ""
}

func (f *channelForm) insert(text string) {
	r := f.row()
	switch {
	case f.text(r) == nil:
	case f.protected(r) != nil:
		f.protectedInsert(text)
	default:
		editText(f.text(r), &f.cursor, func(e *monitorForm) { e.insert(text) })
	}
}
func (f *channelForm) edit(msg tea.KeyPressMsg, snap Snapshot) {
	r := f.row()
	if value := f.choice(r); value != nil {
		if r == rowKind && f.editing {
			return // the kind of a saved channel does not change
		}
		switch msg.String() {
		case "left":
			*value = cycleChoice(f.choices(r, snap), *value, -1)
		case "right":
			*value = cycleChoice(f.choices(r, snap), *value, 1)
		default:
			return
		}
		if r == rowKind {
			f.err = nil
		}
		return
	}
	if f.protected(r) != nil {
		f.protectedEdit(msg)
		return
	}
	editText(f.text(r), &f.cursor, func(e *monitorForm) { e.edit(msg) })
}

// editText applies a monitor form text edit to one channel form field.
func editText(text *string, cursor *int, apply func(*monitorForm)) {
	editor := monitorForm{focus: 0, cursor: *cursor}
	editor.values[0] = *text
	apply(&editor)
	*text, *cursor = editor.values[0], editor.cursor
}

// focusError moves the focus to the row an error names, so the fix starts
// where the cursor is. The error itself shows in the hint slot.
func (f *channelForm) focusError(err error) {
	r, ok := errorRow(err)
	if !ok || (r == rowName && f.editing) {
		return
	}
	for i, row := range f.rows() {
		if row == r {
			f.focus = i
			f.cursor = len([]rune(f.fieldText()))
			return
		}
	}
}

// errorRow is the row a save error is about, from its first line: the
// form's own checks and the store's validation name the field.
func errorRow(err error) (channelRow, bool) {
	text := strings.ToLower(firstLine(err.Error()))
	for _, m := range []struct {
		match string
		row   channelRow
	}{
		{"already exists", rowName}, {"name is required", rowName}, {`name "none"`, rowName},
		{"smtp server", rowSMTP}, {"recipient", rowRecipients},
		{"requires a header name", rowAuthName}, {"auth header name", rowAuthName}, {"auth value", rowAuth},
		{"server url", rowServer}, {"topic", rowTopic}, {"chat id", rowChat}, {"token", rowToken}, {"priority", rowPriority},
		{"url", rowURL}, {"origin", rowURL}, {"webhook values", rowURL},
	} {
		if strings.Contains(text, m.match) {
			return m.row, true
		}
	}
	return rowName, false
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
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
		count := f.fieldCount() - first
		f.focus = first + (f.focus-first+delta+count)%count
		f.cursor = len([]rune(f.fieldText()))
	case "enter":
		channel, err := f.channel(m.snap)
		if err != nil {
			f.err = err
			f.focusError(err)
			return m, nil
		}
		f.err = nil
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
	if m.channelTest != nil {
		return m.handleChannelTestKey(msg)
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
	case "t":
		if m.channelSelected < len(m.snap.Channels) {
			m.channelTest = &channelTestPrompt{name: m.snap.Channels[m.channelSelected].Name}
		}
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

// channel is the channel to save, built from the kind's rows. Email
// channels are checked by the store; the others are checked here first,
// because their protected values are sealed before the store sees them.
func (f channelForm) channel(snap Snapshot) (config.Channel, error) {
	switch f.kind() {
	case config.KindEmail:
		return config.Channel{Name: f.values[0], Kind: config.KindEmail, SMTPServer: f.values[2], Recipients: config.ParseRecipients(f.values[3])}, nil
	case config.KindWebhook:
		return f.webhookChannel(snap)
	}
	return f.pushChannel(snap)
}

// renderChannelForm draws the kind's rows padded to the longest kind, then
// the hint slot, which a save error takes over, so neither the box nor the
// rows move while Kind cycles.
func renderChannelForm(f channelForm, snap Snapshot, now time.Time, loadErr error) string {
	var b strings.Builder
	title := "New channel"
	if f.editing {
		title = "Edit channel"
	}
	b.WriteString(titleStyle.Render(title) + "\n" + renderBanners(snap, now, loadErr))
	rows := f.rows()
	for i, r := range rows {
		marker, label := " ", f.label(r)
		if i == f.focus {
			marker, label = accentStyle.Render(">"), accentStyle.Render(label)
		}
		fmt.Fprintf(&b, "%s %s: %s\n", marker, label, f.renderValue(r, i == f.focus, snap))
	}
	b.WriteString(strings.Repeat("\n", maxKindRows()-len(rows)) + "\n")
	// The hint slot is two lines: the hint and a spacer, or an error that
	// wraps once.
	hint := f.hint(snap)
	if f.err != nil {
		hint = wrapStyled(warnStyle, "Cannot save channel: "+firstLine(f.err.Error()))
	}
	if !strings.Contains(hint, "\n") {
		hint += "\n"
	}
	b.WriteString(hint + "\n")
	r := f.row()
	switch {
	case f.saving:
		b.WriteString(renderHelp("Saving channel… · ctrl+c quit"))
	case f.choice(r) != nil:
		b.WriteString(renderHelp("←/→ choose · tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	case f.protected(r) != nil:
		actions := make([]string, 0, 3)
		for _, a := range f.actions(r) {
			actions = append(actions, editLabel(a))
		}
		b.WriteString(renderHelp("←/→ " + strings.Join(actions, "/") + " · tab/shift+tab focus · enter save · esc cancel"))
	default:
		b.WriteString(renderHelp("tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	}
	return b.String()
}

func (f channelForm) renderValue(r channelRow, focused bool, snap Snapshot) string {
	cursor := func(text string) string {
		if !focused {
			return text
		}
		runes := []rune(text)
		at := min(f.cursor, len(runes))
		return string(runes[:at]) + accentStyle.Render("│") + string(runes[at:])
	}
	switch r {
	case rowKind:
		return renderChoice(f.kind(), nil, focused)
	case rowSMTP:
		if len(smtpNames(snap)) == 0 {
			return helpStyle.Render("(none — press esc twice, then s to add one)")
		}
		return renderChoice(f.values[2], nil, focused)
	case rowPriority:
		return renderChoice(*f.choice(r), nil, focused)
	case rowURL, rowAuth, rowToken:
		p := *f.protected(r)
		shown, origin := p.text, ""
		if r == rowURL {
			origin = f.hook.origin
		} else {
			shown = strings.Repeat("•", len([]rune(p.text)))
		}
		if p.set && (p.edit != config.EditReplace || p.text == "") {
			state := helpStyle.Render("set")
			if origin != "" {
				state = origin + " " + state
			}
			return state + " " + renderChoice(editLabel(p.edit), nil, focused)
		}
		if p.set {
			return cursor(shown) + " " + renderChoice("replace", nil, focused)
		}
		return cursor(shown)
	}
	return cursor(*f.text(r))
}

// hint is the line under the rows: what to type, or a warning.
func (f channelForm) hint(snap Snapshot) string {
	switch kind := f.kind(); {
	case kind != config.KindEmail && !snap.HasKey:
		return warnStyle.Render(noKeyHint)
	case kind == config.KindEmail:
		return helpStyle.Render("comma-separated")
	case kind == config.KindWebhook:
		return helpStyle.Render("Sends a JSON POST. Any 2xx counts as delivered; no retries.")
	}
	return f.pushHint()
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
