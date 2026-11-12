package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
)

// kindPing and kindDNS are the form's types for ping and DNS monitors.
const (
	kindPing = config.CheckPing
	kindDNS  = config.CheckDNS
)

const (
	pingCopy = "One echo request per check; down if no reply within the timeout."
	dnsCopy  = "Down when it does not resolve or no answer equals Expected."
)

// pingDeniedLine warns that the daemon cannot run ping monitors.
const pingDeniedLine = "! up serve cannot send pings. README › Ping says how to allow it."

// Dim text shown while the optional DNS rows are empty.
const (
	resolverHint = "system resolver"
	expectedHint = "any answer"
)

func (f monitorForm) isPing() bool { return f.kind == kindPing }
func (f monitorForm) isDNS() bool  { return f.kind == kindDNS }

func editPingForm(c config.Check, detail bool) *monitorForm {
	return &monitorForm{
		kind:   kindPing,
		values: [5]string{c.Name, c.Host, shortDuration(c.Interval), shortDuration(c.Timeout), ""}, record: config.DNSRecords[0],
		channel: c.Channel, alertAfter: strconv.Itoa(c.FailuresToAlert()), recoverAfter: strconv.Itoa(c.SuccessesToRecover()), tlsExpiry: "on",
		focus: 1, cursor: len([]rune(c.Host)), editing: true, returnDetail: detail, base: c,
	}
}

func editDNSForm(c config.Check, detail bool) *monitorForm {
	return &monitorForm{
		kind:   kindDNS,
		values: [5]string{c.Name, c.Domain, shortDuration(c.Interval), shortDuration(c.Timeout), ""},
		record: c.Record, resolver: c.Resolver, expected: c.Expected,
		channel: c.Channel, alertAfter: strconv.Itoa(c.FailuresToAlert()), recoverAfter: strconv.Itoa(c.SuccessesToRecover()), tlsExpiry: "on",
		focus: 1, cursor: len([]rune(c.Domain)), editing: true, returnDetail: detail, base: c,
	}
}

// probeCheck converts text only, like check, for ping and DNS monitors; it
// carries no HTTP or TCP field, so no TLS choice either.
func (f monitorForm) probeCheck() (config.Check, error) {
	c := config.Check{Name: f.values[0], Kind: f.kind, Channel: f.channel, Tags: config.ParseTags(f.tags)}
	if f.isPing() {
		c.Host = f.values[1]
	} else {
		c.Domain, c.Record, c.Resolver, c.Expected = f.values[1], f.record, f.resolver, f.expected
	}
	if err := f.parseShared(&c); err != nil {
		return c, err
	}
	return c, config.ValidateCheck(c)
}

// parseShared reads the interval, timeout and thresholds the TCP, ping and
// DNS forms share.
func (f monitorForm) parseShared(c *config.Check) error {
	var err error
	if c.Interval, err = time.ParseDuration(f.values[2]); err != nil {
		return fmt.Errorf("interval: %w", err)
	}
	if c.Timeout, err = time.ParseDuration(f.values[3]); err != nil {
		return fmt.Errorf("timeout: %w", err)
	}
	if c.AlertAfter, err = strconv.Atoi(f.alertAfter); err != nil {
		return fmt.Errorf("alert after: %w", err)
	}
	if c.RecoverAfter, err = strconv.Atoi(f.recoverAfter); err != nil {
		return fmt.Errorf("recover after: %w", err)
	}
	return nil
}

// hintRow is textRow with dim text in place of an empty value.
func hintRow(b *strings.Builder, f monitorForm, focus int, label, value, hint string) {
	if value != "" {
		textRow(b, f, focus, label, value)
		return
	}
	marker, shown := " ", helpStyle.Render(hint)
	if focus == f.focus {
		marker, label, shown = accentStyle.Render(">"), accentStyle.Render(label), accentStyle.Render("│")+shown
	}
	fmt.Fprintf(b, "%s %s: %s\n", marker, label, shown)
}

// renderPingFields renders the rows before Channel for a ping monitor.
func renderPingFields(b *strings.Builder, f monitorForm) {
	textRow(b, f, 0, "Name", f.values[0])
	textRow(b, f, 1, "Host", f.values[1])
	textRow(b, f, 2, "Interval", f.values[2])
	textRow(b, f, 3, "Timeout", f.values[3])
	b.WriteString(helpStyle.Render(pingCopy) + "\n")
}

// renderDNSFields renders the rows before Channel for a DNS monitor.
func renderDNSFields(b *strings.Builder, f monitorForm) {
	textRow(b, f, 0, "Name", f.values[0])
	textRow(b, f, 1, "Domain", f.values[1])
	marker, label := " ", "Record"
	if f.focus == focusRecord {
		marker, label = accentStyle.Render(">"), accentStyle.Render(label)
	}
	fmt.Fprintf(b, "%s %s: %s\n", marker, label, renderChoice(f.record, config.DNSRecords, f.focus == focusRecord))
	hintRow(b, f, focusResolver, "Resolver", f.resolver, resolverHint)
	hintRow(b, f, focusExpected, "Expected", f.expected, expectedHint)
	textRow(b, f, 2, "Interval", f.values[2])
	textRow(b, f, 3, "Timeout", f.values[3])
	b.WriteString(helpStyle.Render(dnsCopy) + "\n")
}

// shown is s as the list and detail show it: a ping monitor the daemon
// cannot run is pending, whatever its older results say.
func (snap Snapshot) shown(s Service) Service {
	if snap.Pings.Holds(s.Check) {
		s.HasResult, s.Latest, s.DownSince = false, check.Result{}, time.Time{}
	}
	return s
}

// dnsAnswer is a DNS result's answer with how many more there were:
// "203.0.113.10 +1"; empty for other results.
func dnsAnswer(r check.Result) string {
	if r.Answer == "" {
		return ""
	}
	if r.Answers > 1 {
		return fmt.Sprintf("%s +%d", r.Answer, r.Answers-1)
	}
	return r.Answer
}

// probeTarget is the detail title's target of a ping or DNS monitor:
// "ping 192.168.1.1", "DNS A example.com", with "@resolver" when it is not
// the system one.
func probeTarget(c config.Check) string {
	if c.Kind == kindPing {
		return "ping " + c.Host
	}
	target := "DNS " + c.Target()
	if c.Resolver != "" {
		target += " @" + c.Resolver
	}
	return target
}
