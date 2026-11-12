package tui

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/config"
)

// kindTCP is the form's type for a TCP monitor; the zero value is HTTP.
const kindTCP = config.CheckTCP

var kindChoices = []string{"HTTP", "TCP", "Heartbeat", "Ping", "DNS"}

// kindByName maps a Type choice to the form's kind.
var kindByName = map[string]string{"HTTP": "", "TCP": kindTCP, "Heartbeat": kindHeartbeat, "Ping": kindPing, "DNS": kindDNS}

const tcpCopy = "Checks that the port accepts a connection."

func (f monitorForm) isTCP() bool { return f.kind == kindTCP }

func (f monitorForm) kindName() string {
	switch {
	case f.isTCP():
		return "TCP"
	case f.isHeartbeat():
		return "Heartbeat"
	case f.isPing():
		return "Ping"
	case f.isDNS():
		return "DNS"
	}
	return "HTTP"
}

// choiceFocus reports whether the focused row is chosen with ←/→ instead of typed.
func (f monitorForm) choiceFocus() bool {
	switch f.focus {
	case focusPort, focusPeriod, focusGrace, focusResolver, focusExpected, focusTags, focusCron:
		return false
	}
	return f.focus >= focusChannel && f.focus < focusAdvanced
}

// order is the tab order of the focusable rows, Type first. An existing
// monitor's name is displayed, never focused.
func (f monitorForm) order() []int {
	var rows []int
	switch {
	case f.isTCP():
		rows = []int{focusType, 0, 1, focusPort, 2, 3, focusTags, focusChannel, focusAlertAfter, focusRecoverAfter}
	case f.isHeartbeat():
		rows = []int{focusType, 0, focusSchedule, focusPeriod, focusGrace, focusTags, focusChannel, focusAlertAfter, focusRecoverAfter}
		if f.isCron() {
			rows[3] = focusCron
		}
		if f.tokenRow() {
			rows = append(rows, focusToken)
		}
	case f.isPing():
		rows = []int{focusType, 0, 1, 2, 3, focusTags, focusChannel, focusAlertAfter, focusRecoverAfter}
	case f.isDNS():
		rows = []int{focusType, 0, 1, focusRecord, focusResolver, focusExpected, 2, 3, focusTags, focusChannel, focusAlertAfter, focusRecoverAfter}
	default:
		rows = []int{focusType, 0, 1, 2, 3, 4, focusTags, focusChannel, focusAlertAfter, focusRecoverAfter, focusTLS}
	}
	if f.editing {
		rows = append(rows[:1:1], rows[2:]...)
	}
	if f.kind == "" {
		rows = append(rows, focusAdvanced)
		for i := range f.adv.stops() {
			rows = append(rows, focusCount+i)
		}
	}
	return rows
}

// text returns the focused row's text, or nil when it is a choice.
func (f *monitorForm) text() *string {
	switch {
	case f.focus < len(f.values):
		return &f.values[f.focus]
	case f.focus == focusPort:
		return &f.port
	case f.focus == focusPeriod:
		return &f.period
	case f.focus == focusGrace:
		return &f.grace
	case f.focus == focusCron:
		return &f.cron
	case f.focus == focusResolver:
		return &f.resolver
	case f.focus == focusExpected:
		return &f.expected
	case f.focus == focusTags:
		return &f.tags
	}
	return f.advText()
}

// setKind switches the monitor type. The other type's values are dropped, not
// hidden: the target field, port, expected status, TLS choice, schedule,
// period, cron, grace, the DNS rows and every field carried over from an
// edited monitor start over, keeping only the name.
func (f *monitorForm) setKind(kind string) {
	if f.kind == kind {
		return
	}
	f.kind, f.newToken = kind, false
	f.period, f.grace = shortDuration(config.DefaultHeartbeatPeriod), shortDuration(config.DefaultHeartbeatGrace)
	f.schedule, f.cron = "", ""
	f.values[1], f.port, f.tlsExpiry = "", "", "on"
	f.record, f.resolver, f.expected = config.DNSRecords[0], "", ""
	f.values[4] = ""
	if f.kind == "" || f.isHeartbeat() {
		f.values[4] = cmp.Or(f.status, "200")
	}
	f.base = config.Check{Name: f.base.Name}
	f.adv = advancedForm{}
}

func editTCPForm(c config.Check, detail bool) *monitorForm {
	return &monitorForm{
		kind:   kindTCP,
		values: [5]string{c.Name, c.Host, shortDuration(c.Interval), shortDuration(c.Timeout), ""}, port: strconv.Itoa(c.Port), record: config.DNSRecords[0],
		channel: c.Channel, alertAfter: strconv.Itoa(c.FailuresToAlert()), recoverAfter: strconv.Itoa(c.SuccessesToRecover()), tlsExpiry: "on",
		focus: 1, cursor: len([]rune(c.Host)), editing: true, returnDetail: detail, base: c,
	}
}

// tcpCheck converts text only, like check; it carries no HTTP field.
func (f monitorForm) tcpCheck() (config.Check, error) {
	c := config.Check{Name: f.values[0], Kind: kindTCP, Host: f.values[1], Channel: f.channel, Tags: config.ParseTags(f.tags)}
	var err error
	if c.Port, err = strconv.Atoi(f.port); err != nil {
		return c, fmt.Errorf("port: %w", err)
	}
	if err := f.parseShared(&c); err != nil {
		return c, err
	}
	return c, config.ValidateCheck(c)
}

// typeLine is the first form row.
func typeLine(f monitorForm) string {
	marker, label := " ", "Type"
	if f.focus == focusType {
		marker, label = accentStyle.Render(">"), accentStyle.Render(label)
	}
	return fmt.Sprintf("%s %s: %s\n", marker, label, renderChoice(f.kindName(), kindChoices, f.focus == focusType))
}

// textRow renders one typed row, with the cursor when it is focused.
func textRow(b *strings.Builder, f monitorForm, focus int, label, value string) {
	marker := " "
	if focus == f.focus {
		runes := []rune(value)
		value = string(runes[:f.cursor]) + accentStyle.Render("│") + string(runes[f.cursor:])
		marker, label = accentStyle.Render(">"), accentStyle.Render(label)
	}
	fmt.Fprintf(b, "%s %s: %s\n", marker, label, value)
}

// renderTCPFields renders the rows before Channel for a TCP monitor.
func renderTCPFields(b *strings.Builder, f monitorForm) {
	textRow(b, f, 0, "Name", f.values[0])
	textRow(b, f, 1, "Host", f.values[1])
	textRow(b, f, focusPort, "Port", f.port)
	textRow(b, f, 2, "Interval", f.values[2])
	textRow(b, f, 3, "Timeout", f.values[3])
	b.WriteString(helpStyle.Render(tcpCopy) + "\n")
}

// kindTags are the dim tags after the name of monitors that are not HTTP.
var kindTags = map[string]string{kindTCP: " tcp", kindHeartbeat: " hb", kindPing: " ping", kindDNS: " dns"}

// kindTagWidth is the extra name-column width for the kind tags: 4 cells,
// 5 when a monitor is a ping, 0 when every monitor is HTTP or the terminal
// is too narrow to keep the existing columns.
func kindTagWidth(snap Snapshot, width, nameWidth, statusWidth, certWidth int) int {
	tag := 0
	for _, s := range snap.Services {
		if t, ok := kindTags[s.Check.Kind]; ok {
			tag = max(tag, len(" tcp"), len(t))
		}
	}
	if tag == 0 || (width > 0 && width < max(78, nameWidth+tag+55+statusWidth+certWidth)) {
		return 0
	}
	return tag
}

// tagKind appends the dim kind tag to a monitor's name cell. The name column
// is padded first so the tags line up.
func tagKind(name string, c config.Check, nameWidth, tag int) string {
	if tag == 0 {
		return name
	}
	name += strings.Repeat(" ", max(0, nameWidth-tag-ansi.StringWidth(name)))
	t := kindTags[c.Kind]
	if t == "" {
		return name + strings.Repeat(" ", tag)
	}
	return name + helpStyle.Render(t+strings.Repeat(" ", max(0, tag-len(t))))
}
