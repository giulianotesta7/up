package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/heohlee/up/internal/config"
)

var fieldLabels = [...]string{"Name", "HTTP/HTTPS URL", "Interval", "Timeout", "Expected status"}

// thresholdChoices are the values offered for Alert after and Recover after.
var thresholdChoices = []string{"1", "2", "3", "5", "10"}

var tlsChoices = []string{"on", "off"}

// Focus positions after the text fields: the channel, then the thresholds.
const (
	focusChannel = len(fieldLabels) + iota
	focusAlertAfter
	focusRecoverAfter
	focusTLS
	focusType
	focusPort
	focusPeriod
	focusGrace
	focusToken
	focusRecord
	focusResolver
	focusExpected
	focusTags
	focusAdvanced // HTTP fold row; the fields inside follow (form_advanced.go)
	focusCount
)

type monitorForm struct {
	values       [5]string
	channel      string
	alertAfter   string
	recoverAfter string
	tlsExpiry    string
	kind         string // "" is HTTP, otherwise kindTCP, kindHeartbeat, kindPing or kindDNS
	port         string // TCP only
	period       string // heartbeat only
	grace        string // heartbeat only
	record       string // DNS only, like resolver and expected
	resolver     string
	expected     string
	tags         string       // every kind, as typed: "nas, home"
	adv          advancedForm // HTTP only
	focus        int
	cursor       int // rune offset in the focused field
	err          error
	saving       bool
	editing      bool
	returnDetail bool
	// tagsWidth is the widest the Tags row may be so its hint never widens
	// the box; 0 means popupRowWidth.
	tagsWidth int
	// copyOf is the monitor a new form was started from ("duplicate").
	copyOf string
	// status is the expected status a new HTTP monitor starts with (the
	// default); empty means 200. Switching the type back to HTTP restores it.
	status string
	// base is the monitor being edited. The form shows only some fields, so
	// the rest (request options, host, port) carry over unchanged.
	base config.Check
	// hadToken is set when editing a heartbeat, which offers Token ‹ keep /
	// new ›; newToken is the choice and confirm the question asked after
	// enter on new.
	hadToken, newToken, confirm bool
	// pingToken, once saved, turns the box into the ping URL (replaced: after
	// a new token); the form is done and only closes.
	pingToken string
	replaced  bool
}

// newMonitorForm starts a new monitor from the snapshot's defaults. A default
// channel that no longer exists starts as none.
func newMonitorForm(snap Snapshot) *monitorForm {
	d := snap.NewItemDefaults()
	status := strconv.Itoa(d.ExpectedStatus)
	return &monitorForm{
		values:  [5]string{"", "", shortDuration(d.Interval), shortDuration(d.Timeout), status},
		channel: snap.existingChannel(d.Channel), alertAfter: strconv.Itoa(d.AlertAfter), recoverAfter: strconv.Itoa(d.RecoverAfter),
		tlsExpiry: "on", status: status,
		period: shortDuration(config.DefaultHeartbeatPeriod), grace: shortDuration(config.DefaultHeartbeatGrace),
		record: config.DNSRecords[0],
	}
}

// editMonitorForm edits c. Every kind carries c's tags, which a save without
// them would remove (and with them the tags' mutes and windows).
func editMonitorForm(c config.Check, detail bool) *monitorForm {
	var f *monitorForm
	switch c.Kind {
	case kindTCP:
		f = editTCPForm(c, detail)
	case kindHeartbeat:
		f = editHeartbeatForm(c, detail)
	case kindPing:
		f = editPingForm(c, detail)
	case kindDNS:
		f = editDNSForm(c, detail)
	default:
		f = &monitorForm{
			values:  [5]string{c.Name, c.URL, shortDuration(c.Interval), shortDuration(c.Timeout), strconv.Itoa(c.ExpectedStatus)},
			record:  config.DNSRecords[0],
			channel: c.Channel, alertAfter: strconv.Itoa(c.FailuresToAlert()), recoverAfter: strconv.Itoa(c.SuccessesToRecover()), tlsExpiry: tlsChoice(c.TLSOff),
			adv:   newAdvanced(c),
			focus: 1, cursor: len([]rune(c.URL)), editing: true, returnDetail: detail, base: c,
		}
	}
	f.tags = config.FormatTags(c.Tags)
	return f
}

// copyName is the name a duplicate starts with: "<name> copy", then
// "<name> copy 2", "<name> copy 3"… until it is free in the snapshot.
func copyName(snap Snapshot, name string) string {
	taken := make(map[string]bool, len(snap.Services))
	for _, s := range snap.Services {
		taken[s.Check.Name] = true
	}
	candidate := name + " copy"
	for n := 2; taken[candidate]; n++ {
		candidate = fmt.Sprintf("%s copy %d", name, n)
	}
	return candidate
}

// copyMonitorForm is the new monitor form prefilled from c. Protected values
// show as set with keep, replace and clear, like the edit form; they are not
// copied here but by the store when the form is saved.
func copyMonitorForm(c config.Check, snap Snapshot) *monitorForm {
	f := editMonitorForm(c, false)
	f.editing, f.copyOf, f.hadToken = false, c.Name, false
	f.values[0] = copyName(snap, c.Name)
	f.focus, f.cursor = 0, len([]rune(f.values[0]))
	return f
}

// check converts text only; all semantic rules belong to config.ValidateCheck.
func (f monitorForm) check() (config.Check, error) { return f.checkWith(nil) }

// checkWith is check with the daemon public key that new protected values are
// sealed to. TCP monitors have no protected values.
func (f monitorForm) checkWith(key []byte) (config.Check, error) {
	if f.isTCP() {
		return f.tcpCheck()
	}
	if f.isHeartbeat() {
		return f.heartbeatCheck()
	}
	if f.isPing() || f.isDNS() {
		return f.probeCheck()
	}
	c := f.base
	c.Name, c.URL, c.Channel, c.Tags = f.values[0], f.values[1], f.channel, config.ParseTags(f.tags)
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
	if c.AlertAfter, err = strconv.Atoi(f.alertAfter); err != nil {
		return c, fmt.Errorf("alert after: %w", err)
	}
	if c.RecoverAfter, err = strconv.Atoi(f.recoverAfter); err != nil {
		return c, fmt.Errorf("recover after: %w", err)
	}
	c.TLSOff = f.tlsExpiry == "off"
	if err := f.applyAdvanced(&c, key); err != nil {
		return c, err
	}
	return c, config.ValidateCheck(c)
}

func tlsChoice(off bool) string {
	if off {
		return "off"
	}
	return "on"
}

type saveMsg struct {
	generation uint64
	name       string
	err        error
	// pingToken is a heartbeat's new token, to show once; tokenErr says why
	// it could not be made after the monitor was saved.
	pingToken string
	tokenErr  error
	replaced  bool
}

// save writes c. A heartbeat that is new (or newly a heartbeat) or whose
// token is replaced then gets a fresh token, which needs no private key, so
// its URL can be shown once; the one made with the monitor was never shown.
func (m Model) save(c config.Check) tea.Cmd {
	f := *m.form
	tokens := m.heartbeatTokens()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		var err error
		switch copier, canCopy := m.store.(CheckCopier); {
		case m.form.editing:
			err = m.store.UpdateCheck(ctx, c)
		case m.form.copyOf == "":
			err = m.store.AddCheck(ctx, c)
		case canCopy:
			err = copier.AddCheckFrom(ctx, m.form.copyOf, c)
		default:
			err = errors.New("this server cannot copy monitors")
		}
		msg := saveMsg{generation: m.generation, name: c.Name, err: err}
		if err != nil || c.Kind != kindHeartbeat || (f.hadToken && !f.newToken) {
			return msg
		}
		msg.replaced = f.hadToken
		if tokens == nil {
			msg.tokenErr = errors.New("this server cannot show ping URLs")
		} else {
			msg.pingToken, msg.tokenErr = tokens.RegenerateHeartbeatToken(ctx, c.Name)
		}
		return msg
	}
}

func (m Model) handleFormKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Copy the pointed-to form so Update never mutates an older model.
	f := *m.form
	m.form = &f
	if f.saving {
		return m, nil
	}
	if f.pingToken != "" {
		if key := msg.String(); key == "esc" || key == "enter" {
			m.form = nil
		}
		return m, nil
	}
	if f.confirm {
		switch msg.String() {
		case "y":
			return m.submitForm(&f)
		case "n", "esc":
			f.confirm, f.newToken = false, false
		}
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
		order := f.order()
		at := 0
		for i, focus := range order {
			if focus == f.focus {
				at = i
			}
		}
		f.focus = order[(at+delta+len(order))%len(order)]
		f.cursor = f.focusCursor()
	case "enter":
		if f.tokenRow() && f.newToken {
			if _, err := f.checkWith(m.snap.PublicKey); err != nil {
				f.err = err
				return m, nil
			}
			f.err, f.confirm = nil, true
			return m, nil
		}
		return m.submitForm(&f)
	default:
		if f.handleAdvancedKey(msg, m.snap.HasKey) {
			return m, nil
		}
		if f.choiceFocus() {
			delta := 0
			switch msg.String() {
			case "left":
				delta = -1
			case "right":
				delta = 1
			}
			switch {
			case delta == 0:
			case f.focus == focusType:
				f.setKind(kindByName[cycleChoice(kindChoices, f.kindName(), delta)])
			case f.focus == focusRecord:
				f.record = cycleChoice(config.DNSRecords, f.record, delta)
			case f.focus == focusToken:
				f.newToken = cycleChoice(tokenChoices, tokenChoice(f.newToken), delta) == "new"
			case f.focus == focusChannel:
				f.channel = cycleChoice(channelChoices(m.snap), f.channel, delta)
			case f.focus == focusAlertAfter:
				f.alertAfter = cycleChoice(thresholdChoices, f.alertAfter, delta)
			case f.focus == focusRecoverAfter:
				f.recoverAfter = cycleChoice(thresholdChoices, f.recoverAfter, delta)
			default:
				f.tlsExpiry = cycleChoice(tlsChoices, f.tlsExpiry, delta)
			}
		} else {
			f.edit(msg)
		}
	}
	return m, nil
}

// submitForm validates f and starts saving it.
func (m Model) submitForm(f *monitorForm) (tea.Model, tea.Cmd) {
	c, err := f.checkWith(m.snap.PublicKey)
	f.err, f.confirm = err, false
	if err != nil {
		f.focusError(err)
		return m, nil
	}
	f.saving = true
	// Invalidate the pre-save read/tick chain before any database write.
	m.generation++
	return m, m.save(c)
}

func tokenChoice(fresh bool) string {
	if fresh {
		return "new"
	}
	return "keep"
}

func channelChoices(snap Snapshot) []string {
	choices := make([]string, 0, len(snap.Channels)+1)
	for _, c := range snap.Channels {
		choices = append(choices, c.Name)
	}
	sort.Strings(choices)
	return append([]string{""}, choices...)
}

// detectionHint says how long a real outage takes to reach the first alert.
func detectionHint(f monitorForm) string {
	n, err := strconv.Atoi(f.alertAfter)
	interval, ierr := time.ParseDuration(f.values[2])
	if err != nil || ierr != nil || n < 2 || interval <= 0 {
		return ""
	}
	return fmt.Sprintf("Detection time ≈ %d × %s = %s before the first alert", n, shortDuration(interval), shortDuration(time.Duration(n)*interval))
}

func (f *monitorForm) insert(text string) {
	field := f.text()
	if field == nil {
		return
	}
	// Paste is single-line too. Ignore terminal controls, not Unicode text.
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	value := []rune(*field)
	inserted := []rune(text)
	value = append(value[:f.cursor], append(inserted, value[f.cursor:]...)...)
	f.cursor += len(inserted)
	*field = string(value)
}

func (f *monitorForm) edit(msg tea.KeyPressMsg) {
	field := f.text()
	if field == nil {
		return
	}
	value := []rune(*field)
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
			*field = string(value)
		}
	case "delete":
		if f.cursor < len(value) {
			*field = string(append(value[:f.cursor], value[f.cursor+1:]...))
		}
	default:
		if msg.Text != "" {
			f.insert(msg.Text)
		}
	}
}

func renderForm(f monitorForm, snap Snapshot, now time.Time, loadErr error) string {
	s, _ := renderFormFocus(f, snap, now, loadErr)
	return s
}

// renderFormFocus also returns the line the focused field is on, so a tall
// form can scroll to it.
func renderFormFocus(f monitorForm, snap Snapshot, now time.Time, loadErr error) (string, int) {
	var b strings.Builder
	focusLine := 0
	title := "New monitor"
	if f.editing {
		title = "Edit monitor"
	} else if f.copyOf != "" {
		title = "New monitor · copy of " + f.copyOf
	}
	b.WriteString(titleStyle.Render(title) + "\n")
	b.WriteString(renderBanners(snap, now, loadErr))
	b.WriteString(typeLine(f))
	for i, label := range fieldLabels {
		if f.isTCP() {
			renderTCPFields(&b, f)
			break
		}
		if f.isHeartbeat() {
			renderHeartbeatFields(&b, f)
			break
		}
		if f.isPing() {
			renderPingFields(&b, f)
			break
		}
		if f.isDNS() {
			renderDNSFields(&b, f)
			break
		}
		marker := " "
		value := f.values[i]
		if i == f.focus {
			marker = ">"
			runes := []rune(value)
			value = string(runes[:f.cursor]) + accentStyle.Render("│") + string(runes[f.cursor:])
			label = accentStyle.Render(label)
			marker = accentStyle.Render(marker)
			focusLine = strings.Count(b.String(), "\n")
		}
		fmt.Fprintf(&b, "%s %s: %s\n", marker, label, value)
	}
	choices := channelChoices(snap)
	choices[0] = "none"
	selected := f.channel
	if selected == "" {
		selected = "none"
	}
	focused := f.choiceFocus()
	choiceLine := func(position int, label, rendered string) {
		marker := " "
		if f.focus == position {
			marker, label = accentStyle.Render(">"), accentStyle.Render(label)
			focusLine = strings.Count(b.String(), "\n")
		}
		fmt.Fprintf(&b, "%s %s: %s\n", marker, label, rendered)
	}
	if f.focus == focusTags {
		focusLine = strings.Count(b.String(), "\n")
	}
	b.WriteString(tagsLine(f, snap))
	choiceLine(focusChannel, "Channel", renderChoice(selected, choices, f.focus == focusChannel))
	failures, successes := "failures in a row", "successes in a row"
	if f.isHeartbeat() {
		failures, successes = "missed pings in a row", "pings in a row"
	}
	choiceLine(focusAlertAfter, "Alert after", renderChoiceUnit(f.alertAfter, failures, thresholdChoices, f.focus == focusAlertAfter))
	choiceLine(focusRecoverAfter, "Recover after", renderChoiceUnit(f.recoverAfter, successes, thresholdChoices, f.focus == focusRecoverAfter))
	if f.tokenRow() {
		choiceLine(focusToken, "Token", renderChoice(tokenChoice(f.newToken), tokenChoices, f.focus == focusToken))
	}
	if f.kind == "" {
		choiceLine(focusTLS, "TLS expiry", renderChoiceUnit(f.tlsExpiry, "", tlsChoices, f.focus == focusTLS)+"   "+helpStyle.Render("HTTPS only"))
		f.renderAdvanced(&b, snap.HasKey, &focusLine)
	}
	if f.confirm {
		renderTokenConfirm(&b)
		return b.String(), focusLine
	}
	if f.isHeartbeat() {
		b.WriteString("\n")
		if hint := firstAlertHint(f); hint != "" {
			b.WriteString(helpStyle.Render(hint) + "\n")
		}
		if !snap.API.Enabled() {
			b.WriteString(warnStyle.Render(noListenerLine) + "\n")
		}
	} else {
		hint, denied := detectionHint(f), f.isPing() && snap.Pings == config.PingDenied
		if hint != "" || denied {
			b.WriteString("\n")
		}
		if hint != "" {
			b.WriteString(helpStyle.Render(hint) + "\n")
		}
		if denied {
			b.WriteString(warnStyle.Render(pingDeniedLine) + "\n")
		}
	}
	if f.err != nil {
		b.WriteString("\n" + warnStyle.Render("Cannot save monitor: "+f.err.Error()) + "\n")
	}
	if f.saving {
		b.WriteString("\n" + renderHelp("Saving monitor… · ctrl+c quit"))
	} else if f.focus >= focusAdvanced {
		b.WriteString("\n" + renderHelp(f.advancedHelp()))
	} else if focused {
		b.WriteString("\n" + renderHelp("←/→ choose · tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	} else {
		b.WriteString("\n" + renderHelp("tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"))
	}
	return b.String(), focusLine
}
