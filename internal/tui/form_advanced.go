package tui

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
)

// editChoices are the actions offered for a protected value that is set.
var editChoices = []string{"keep", "replace", "clear"}

// protectedField is a header value or request body. Stored text is never
// shown: when a value is set the form says "set" and offers keep, replace and
// clear. New text is shown as bullets and sealed to the daemon key on save.
type protectedField struct {
	had    bool
	stored secret.Sealed // as read; sent back untouched on keep
	action config.Edit
	text   string
}

func newProtected(v secret.Sealed) protectedField {
	return protectedField{had: !v.Empty(), stored: v}
}

// cycle changes the action of a set value.
func (p *protectedField) cycle(delta int) {
	if !p.had {
		return
	}
	names := cycleChoice(editChoices, p.action.String(), delta)
	p.action, _ = config.ParseEdit(names)
	if p.action != config.EditReplace {
		p.text = ""
	}
}

func (p *protectedField) edit(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "left":
		p.cycle(-1)
	case "right":
		p.cycle(1)
	case "backspace":
		if r := []rune(p.text); len(r) > 0 {
			p.text = string(r[:len(r)-1])
		}
		if p.text == "" && p.action == config.EditReplace {
			p.action = config.EditKeep
		}
	default:
		if msg.Text == "" {
			return
		}
		p.text += strings.Map(func(r rune) rune {
			if r < ' ' || r == 0x7f {
				return -1
			}
			return r
		}, msg.Text)
		if p.had {
			p.action = config.EditReplace
		}
	}
}

// seal turns the field into the stored value and edit for a write.
// absent is the edit used when nothing is set and nothing was typed.
func (p protectedField) seal(key []byte, absent config.Edit) (secret.Sealed, config.Edit, error) {
	switch {
	case p.had && p.action == config.EditClear:
		return secret.Sealed{}, config.EditClear, nil
	case p.text != "":
		if len(key) == 0 {
			return secret.Sealed{}, 0, errors.New("protected values need the encryption key: start `up serve` once, then come back")
		}
		v, err := secret.Seal(key, p.text)
		return v, config.EditReplace, err
	case p.had:
		return p.stored, config.EditKeep, nil
	}
	return secret.Sealed{}, absent, nil
}

type headerRow struct {
	name  string
	orig  string // stored name, to notice a rename
	value protectedField
}

// condRow is one response condition. The fields of every kind are kept while
// the kind is cycled; only the chosen kind's fields are saved.
type condRow struct {
	kind                               string
	path, name, value, pattern, within string
}

// advancedForm is the optional HTTP request section. The zero value is a
// closed fold with GET and nothing else.
type advancedForm struct {
	open    bool
	method  string // "" means GET
	headers []headerRow
	body    protectedField
	expect  string
	conds   []condRow
}

func newAdvanced(c config.Check) advancedForm {
	a := advancedForm{method: c.Method, body: newProtected(c.Body), expect: c.ExpectText}
	for _, h := range c.Headers {
		a.headers = append(a.headers, headerRow{name: h.Name, orig: h.Name, value: newProtected(h.Value)})
	}
	for _, cond := range c.Conditions {
		row := condRow{kind: cond.Kind, path: cond.Path, name: cond.Name, value: cond.Value, pattern: cond.Pattern}
		if cond.Within > 0 {
			row.within = config.ShortDuration(cond.Within)
		}
		a.conds = append(a.conds, row)
	}
	a.open = a.configured()
	return a
}

func (a advancedForm) methodName() string {
	if a.method == "" {
		return "GET"
	}
	return a.method
}

// configured reports whether any advanced setting differs from the default.
func (a advancedForm) configured() bool {
	return a.methodName() != "GET" || len(a.headers) > 0 || a.body.had || a.body.text != "" || a.expect != "" || len(a.conds) > 0
}

// Advanced stops follow focusAdvanced while the fold is open.
type advStopKind int

const (
	stopMethod advStopKind = iota
	stopHeaderName
	stopHeaderValue
	stopAdd
	stopBody
	stopExpect
	stopCondKind
	stopCondPath
	stopCondName
	stopCondValue
	stopCondPattern
	stopCondWithin
	stopCondAdd
)

// condFields are the stops after a condition's kind row, by kind.
var condFields = map[string][]advStopKind{
	config.ConditionJSON:         {stopCondPath, stopCondValue},
	config.ConditionRegex:        {stopCondPattern},
	config.ConditionResponseTime: {stopCondWithin},
	config.ConditionHeader:       {stopCondName, stopCondValue},
}

type advStop struct {
	kind advStopKind
	row  int
}

func (a advancedForm) stops() []advStop {
	if !a.open {
		return nil
	}
	out := []advStop{{kind: stopMethod}}
	for i := range a.headers {
		out = append(out, advStop{stopHeaderName, i}, advStop{stopHeaderValue, i})
	}
	out = append(out, advStop{kind: stopAdd}, advStop{kind: stopBody}, advStop{kind: stopExpect})
	for i, c := range a.conds {
		out = append(out, advStop{stopCondKind, i})
		for _, kind := range condFields[c.kind] {
			out = append(out, advStop{kind, i})
		}
	}
	return append(out, advStop{kind: stopCondAdd})
}

// focusStop moves the focus to the first advanced stop of row with one of
// kinds, or to the row's kind row when it has none of them.
func (f *monitorForm) focusStop(row int, kinds ...advStopKind) {
	at := -1
	for i, s := range f.adv.stops() {
		if s.row != row || s.kind < stopCondKind || s.kind == stopCondAdd {
			continue
		}
		if at < 0 || slices.Contains(kinds, s.kind) {
			at = i
		}
		if slices.Contains(kinds, s.kind) {
			break
		}
	}
	if at >= 0 {
		f.focus = focusCount + at
		f.cursor = f.focusCursor()
	}
}

// condText is the condition text field of stop s.
func (a *advancedForm) condText(s advStop) *string {
	c := &a.conds[s.row]
	switch s.kind {
	case stopCondPath:
		return &c.path
	case stopCondName:
		return &c.name
	case stopCondValue:
		return &c.value
	case stopCondPattern:
		return &c.pattern
	case stopCondWithin:
		return &c.within
	}
	return nil
}

func (f monitorForm) stop() (advStop, bool) {
	stops := f.adv.stops()
	if i := f.focus - focusCount; i >= 0 && i < len(stops) {
		return stops[i], true
	}
	return advStop{}, false
}

// text returns the plain text field under the focus, if it is one.
func (f *monitorForm) advText() *string {
	s, ok := f.stop()
	switch {
	case ok && s.kind == stopHeaderName:
		return &f.adv.headers[s.row].name
	case ok && s.kind == stopExpect:
		return &f.adv.expect
	case ok && s.kind > stopCondKind && s.kind < stopCondAdd:
		return f.adv.condText(s)
	}
	return nil
}

// focusCursor is where the cursor sits when the focus moves to a field.
func (f *monitorForm) focusCursor() int {
	if p := f.text(); p != nil {
		return len([]rune(*p))
	}
	return 0
}

// handleAdvancedKey handles keys on the fold row and inside the fold. It
// reports false for focus positions that belong to the basic fields.
func (f *monitorForm) handleAdvancedKey(msg tea.KeyPressMsg, hasKey bool) bool {
	if f.focus < focusAdvanced {
		return false
	}
	f.adv.headers = slices.Clone(f.adv.headers)
	f.adv.conds = slices.Clone(f.adv.conds)
	key := msg.String()
	if f.focus == focusAdvanced {
		switch key {
		case "space", "right":
			f.adv.open = true
		case "left":
			f.adv.open = false
		}
		return true
	}
	s, ok := f.stop()
	if !ok {
		return true
	}
	switch s.kind {
	case stopMethod:
		switch key {
		case "left":
			f.adv.method = cycleChoice(config.HTTPMethods, f.adv.methodName(), -1)
		case "right":
			f.adv.method = cycleChoice(config.HTTPMethods, f.adv.methodName(), 1)
		}
	case stopAdd:
		if key == "space" || key == "right" || key == "+" {
			f.adv.headers = append(f.adv.headers, headerRow{})
			f.focus = focusCount + 1 + 2*(len(f.adv.headers)-1)
			f.cursor = 0
		}
	case stopHeaderName:
		if key == "ctrl+d" {
			f.removeHeader(s.row)
			return true
		}
		f.editText(msg)
	case stopHeaderValue:
		if key == "ctrl+d" {
			f.removeHeader(s.row)
		} else if hasKey || msg.Text == "" {
			f.adv.headers[s.row].value.edit(msg)
		}
	case stopBody:
		if hasKey || msg.Text == "" {
			f.adv.body.edit(msg)
		}
	case stopExpect:
		f.editText(msg)
	case stopCondAdd:
		if key == "space" || key == "right" || key == "+" {
			f.adv.conds = append(f.adv.conds, condRow{kind: config.ConditionJSON})
			f.focusStop(len(f.adv.conds) - 1)
		}
	case stopCondKind:
		switch key {
		case "ctrl+d":
			f.removeCondition(s.row)
		case "left", "right":
			delta := map[string]int{"left": -1, "right": 1}[key]
			f.adv.conds[s.row].kind = cycleChoice(config.ConditionKinds, f.adv.conds[s.row].kind, delta)
		}
	default:
		if key == "ctrl+d" {
			f.removeCondition(s.row)
			return true
		}
		f.editText(msg)
	}
	return true
}

// removeCondition deletes a condition and focuses the next one, or the add
// row.
func (f *monitorForm) removeCondition(row int) {
	f.adv.conds = slices.Delete(f.adv.conds, row, row+1)
	if row < len(f.adv.conds) {
		f.focusStop(row)
		return
	}
	f.focus = focusCount + len(f.adv.stops()) - 1
	f.cursor = 0
}

func (f *monitorForm) editText(msg tea.KeyPressMsg) {
	p := f.advText()
	if p == nil {
		return
	}
	editor := monitorForm{cursor: f.cursor}
	editor.values[0] = *p
	editor.edit(msg)
	*p, f.cursor = editor.values[0], editor.cursor
}

func (f *monitorForm) removeHeader(row int) {
	f.adv.headers = slices.Delete(f.adv.headers, row, row+1)
	f.focus = focusCount + 1 + 2*row // the next row, or the add row
	f.cursor = f.focusCursor()
}

// applyAdvanced copies the advanced section into c. key is the daemon public
// key new protected values are sealed to. TCP monitors have no such section.
func (f monitorForm) applyAdvanced(c *config.Check, key []byte) error {
	if c.Kind == config.CheckTCP {
		return nil
	}
	a := f.adv
	c.Method, c.ExpectText = a.method, a.expect
	c.Headers = nil
	for _, row := range a.headers {
		h := config.Header{Name: row.name}
		if row.value.had && row.value.action == config.EditKeep && !strings.EqualFold(row.name, row.orig) {
			return fmt.Errorf("header %q was renamed: replace or clear its value", row.name)
		}
		var err error
		if h.Value, h.Edit, err = row.value.seal(key, config.EditClear); err != nil {
			return err
		}
		c.Headers = append(c.Headers, h)
	}
	c.Conditions = nil
	for i, row := range a.conds {
		cond := config.Condition{Kind: row.kind}
		switch row.kind {
		case config.ConditionJSON:
			cond.Path, cond.Value = row.path, row.value
		case config.ConditionRegex:
			cond.Pattern = row.pattern
		case config.ConditionHeader:
			cond.Name, cond.Value = row.name, row.value
		case config.ConditionResponseTime:
			if row.within == "" {
				break
			}
			d, err := time.ParseDuration(row.within)
			if err != nil {
				return fmt.Errorf("condition %d: max response time %q is not valid: use a duration like 800ms", i+1, row.within)
			}
			cond.Within = d
		}
		c.Conditions = append(c.Conditions, cond)
	}
	var err error
	c.Body, c.BodyEdit, err = a.body.seal(key, config.EditKeep)
	return err
}

// conditionError matches the position a save error names.
var conditionError = regexp.MustCompile(`(?m)^condition (\d+): (.*)$`)

// focusError moves the focus to the condition field (or the Cron row) a
// save error names, if it names one.
func (f *monitorForm) focusError(err error) {
	if err == nil {
		return
	}
	if f.isCron() {
		f.focusCronError(err)
		return
	}
	if f.kind != "" {
		return
	}
	found := conditionError.FindStringSubmatch(err.Error())
	if found == nil {
		return
	}
	n, _ := strconv.Atoi(found[1])
	if n < 1 || n > len(f.adv.conds) {
		return
	}
	var field advStopKind
	switch text := found[2]; {
	case strings.HasPrefix(text, "JSON path"):
		field = stopCondPath
	case strings.HasPrefix(text, "body regex"):
		field = stopCondPattern
	case strings.HasPrefix(text, "max response time"):
		field = stopCondWithin
	case strings.HasPrefix(text, "header name"):
		field = stopCondName
	case strings.HasPrefix(text, "JSON value"), strings.HasPrefix(text, "header value"):
		field = stopCondValue
	default:
		field = stopCondKind
	}
	f.adv.open = true
	f.focusStop(n-1, field)
}

func (a advancedForm) valueView(p protectedField, focused, hasKey bool) string {
	switch {
	case p.had && p.action == config.EditReplace:
		return strings.Repeat("•", len([]rune(p.text))) + " " + renderChoice("replace", editChoices, focused)
	case p.had:
		return helpStyle.Render("set") + " " + renderChoice(p.action.String(), editChoices, focused)
	case !hasKey:
		return helpStyle.Render("(unavailable until `up serve` runs)")
	}
	value := strings.Repeat("•", len([]rune(p.text)))
	if focused {
		value += accentStyle.Render("│")
	}
	return value
}

// renderAdvanced writes the fold row and, when open, its fields. focusLine
// receives the line of b where the focused field starts.
func (f monitorForm) renderAdvanced(b *strings.Builder, hasKey bool, focusLine *int) {
	line := func(position int, label, rendered string, indent string) {
		marker := " "
		if f.focus == position {
			marker, label = accentStyle.Render(">"), accentStyle.Render(label)
			*focusLine = strings.Count(b.String(), "\n")
		}
		fmt.Fprintf(b, "%s %s%s: %s\n", marker, indent, label, rendered)
	}
	arrow, tail := "▸", ""
	if f.adv.open {
		arrow = "▾"
	}
	if f.adv.configured() {
		tail = "  " + upStyle.Render("configured")
	} else if !f.adv.open {
		tail = "  " + helpStyle.Render("optional · space to open")
	}
	marker, label := " ", "Advanced"
	if f.focus == focusAdvanced {
		marker, label = accentStyle.Render(">"), accentStyle.Render(label)
		*focusLine = strings.Count(b.String(), "\n")
	}
	fmt.Fprintf(b, "%s %s %s%s\n", marker, arrow, label, tail)
	if !f.adv.open {
		return
	}
	base := focusCount
	line(base, "Method", renderChoice(f.adv.methodName(), config.HTTPMethods, f.focus == base), "  ")
	base++
	for i, row := range f.adv.headers {
		name := row.name
		if f.focus == base {
			r := []rune(name)
			name = string(r[:f.cursor]) + accentStyle.Render("│") + string(r[f.cursor:])
		}
		line(base, fmt.Sprintf("Header %d name", i+1), name, "  ")
		line(base+1, fmt.Sprintf("Header %d value", i+1), f.adv.valueView(row.value, f.focus == base+1, hasKey), "  ")
		base += 2
	}
	add := helpStyle.Render("space to add")
	if f.focus == base {
		add = "space to add"
	}
	line(base, "+ Header", add, "  ")
	line(base+1, "Request body", f.adv.valueView(f.adv.body, f.focus == base+1, hasKey), "  ")
	expect := f.adv.expect
	if f.focus == base+2 {
		r := []rune(expect)
		expect = string(r[:f.cursor]) + accentStyle.Render("│") + string(r[f.cursor:])
	}
	line(base+2, "Expected text", expect, "  ")
	base += 3
	labels := make([]string, len(config.ConditionKinds))
	for i, kind := range config.ConditionKinds {
		labels[i] = config.ConditionLabel(kind)
	}
	for i, c := range f.adv.conds {
		line(base, fmt.Sprintf("Condition %d", i+1), renderChoice(config.ConditionLabel(c.kind), labels, f.focus == base), "  ")
		base++
		for _, kind := range condFields[c.kind] {
			text := *f.adv.condText(advStop{kind, i})
			line(base, condFieldLabels[kind], f.textView(text, condHints[kind], f.focus == base), "    ")
			base++
		}
	}
	add = helpStyle.Render("space to add · all must pass")
	if f.focus == base {
		add = "space to add · all must pass"
	}
	line(base, "+ Condition", add, "  ")
}

var condFieldLabels = map[advStopKind]string{
	stopCondPath: "Path", stopCondName: "Name", stopCondValue: "Equals", stopCondPattern: "Pattern", stopCondWithin: "At most",
}

// condHints show the format of an empty field.
var condHints = map[advStopKind]string{
	stopCondPath:   "e.g. services.0.status",
	stopCondWithin: "e.g. 800ms",
}

// textView is a text field's value, with the cursor when focused and the
// hint while it is empty.
func (f monitorForm) textView(text, hint string, focused bool) string {
	if focused {
		r := []rune(text)
		text = string(r[:f.cursor]) + accentStyle.Render("│") + string(r[f.cursor:])
	}
	if hint != "" && (text == "" || text == accentStyle.Render("│")) {
		text += helpStyle.Render(hint)
	}
	return text
}

// advancedHelp is the footer for the focused advanced position.
func (f monitorForm) advancedHelp() string {
	const tail = "tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"
	if f.focus == focusAdvanced {
		return "space fold · " + tail
	}
	s, ok := f.stop()
	switch {
	case !ok:
		return tail
	case s.kind == stopMethod:
		return "←/→ choose · " + tail
	case s.kind == stopAdd:
		return "space add · " + tail
	case s.kind == stopHeaderValue || s.kind == stopBody:
		return "←/→ action · " + tail
	case s.kind == stopHeaderName:
		return "ctrl+d del · " + tail
	case s.kind == stopCondAdd:
		return "space add · " + tail
	case s.kind == stopCondKind:
		// Without ctrl+c quit, so the keys fit an 80-column terminal.
		return "←/→ kind · ctrl+d del · tab/shift+tab focus · enter save · esc cancel"
	case s.kind > stopCondKind:
		return "ctrl+d del · " + tail
	}
	return tail
}

// scrollForm fits a form screen (title line, body, blank line and footer)
// into a modal on a terminal of the given height, keeping the title and the
// footer keys in view and scrolling the body around the focused line.
func scrollForm(screen string, focusLine, height int) string {
	lines := strings.Split(screen, "\n")
	if height <= 0 || len(lines)+3 <= height || len(lines) < 5 {
		return screen
	}
	title, footer := lines[0], lines[len(lines)-2:]
	body := lines[1 : len(lines)-2]
	room := height - 4 - len(footer)
	if room < 3 {
		return screen
	}
	focus := min(max(focusLine-1, 0), len(body)-1)
	start := min(max(focus-room/2, 0), len(body)-room)
	if start > 0 && focus <= start {
		start = focus - 1
	}
	if start+room < len(body) && focus >= start+room-1 {
		start = focus - room + 2
	}
	start = min(max(start, 0), len(body)-room)
	window := slices.Clone(body[start : start+room])
	if start > 0 {
		window[0] = helpStyle.Render(fmt.Sprintf("  ↑ %d more", start))
	}
	if end := start + room; end < len(body) {
		window[room-1] = helpStyle.Render(fmt.Sprintf("  ↓ %d more", len(body)-end))
	}
	return strings.Join(append(append([]string{title}, window...), footer...), "\n")
}

// renderRequestFields adds the advanced request settings that are set to the
// detail view, one line per condition cut to width. Protected values (header
// values, body) are never shown.
func renderRequestFields(field func(label, value string), c config.Check, width int) {
	if c.Method != "" && c.Method != "GET" {
		field("Method", c.Method)
	}
	if c.ExpectText != "" {
		field("Text", fmt.Sprintf("contains %q", c.ExpectText))
	}
	label := "Expect"
	for _, cond := range c.Conditions {
		summary := cond.Summary()
		if width > 0 {
			summary = ansi.Truncate(summary, max(width-15, 10), "…")
		}
		field(label, summary)
		label = ""
	}
}
