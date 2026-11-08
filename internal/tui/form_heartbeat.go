package tui

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/heohlee/up/internal/config"
)

// kindHeartbeat is the form's type for a heartbeat monitor.
const kindHeartbeat = config.CheckHeartbeat

const heartbeatCopy = "Alerts when no ping arrives within period + grace."

const cronCopy = "Alerts when no ping arrives within grace after a run."

// cronHint is the Cron row's field order, shown while it has focus.
const cronHint = "min hour day month weekday"

// scheduleChoices are the Schedule row's choices.
var scheduleChoices = []string{"Period", "Cron"}

// cronLabelWidth lines up the values of the Zone and Next runs lines.
const cronLabelWidth = len("Next runs  ")

// noListenerLine warns that pings have nowhere to arrive.
const noListenerLine = "! No API listener: pings cannot arrive. Run up serve --api-listen"

// tokenChoices are the Token row's choices in Edit monitor.
var tokenChoices = []string{"keep", "new"}

// pingTokenWidth is the length of a ping token ("uphb_" and 43 characters),
// so the ping URL takes the same room masked or shown.
const pingTokenWidth = 48

const pingMask = "••••••••"

func (f monitorForm) isHeartbeat() bool { return f.kind == kindHeartbeat }

// isCron reports whether the form is a heartbeat with Schedule ‹ Cron ›.
func (f monitorForm) isCron() bool { return f.isHeartbeat() && f.schedule == config.ScheduleCron }

func (f monitorForm) scheduleName() string {
	if f.schedule == config.ScheduleCron {
		return "Cron"
	}
	return "Period"
}

// tokenRow reports whether the form offers Token ‹ keep / new ›: only when
// editing a monitor that already is a heartbeat.
func (f monitorForm) tokenRow() bool { return f.isHeartbeat() && f.hadToken }

// editHeartbeatForm edits a heartbeat. Interval and timeout start at the
// built-in defaults in case the type is switched; a cron heartbeat's period
// starts at the new heartbeat default in case the schedule is.
func editHeartbeatForm(c config.Check, detail bool) *monitorForm {
	d := config.BuiltinDefaults()
	f := &monitorForm{
		kind:   kindHeartbeat,
		values: [5]string{c.Name, "", shortDuration(d.Interval), shortDuration(d.Timeout)}, period: shortDuration(c.Period), grace: shortDuration(c.Grace),
		channel: c.Channel, alertAfter: strconv.Itoa(c.FailuresToAlert()), recoverAfter: strconv.Itoa(c.SuccessesToRecover()), tlsExpiry: "on",
		focus: focusPeriod, editing: true, returnDetail: detail, base: c, hadToken: true,
	}
	if c.Schedule == config.ScheduleCron {
		f.schedule, f.cron, f.focus = config.ScheduleCron, c.Cron, focusCron
		f.period = shortDuration(config.DefaultHeartbeatPeriod)
	}
	f.cursor = f.focusCursor()
	return f
}

// heartbeatCheck converts text only, like check; it carries no probe field.
func (f monitorForm) heartbeatCheck() (config.Check, error) {
	c := config.Check{Name: f.values[0], Kind: kindHeartbeat, Channel: f.channel}
	var err error
	if f.isCron() {
		c.Schedule, c.Cron = config.ScheduleCron, f.cron
	} else if c.Period, err = time.ParseDuration(f.period); err != nil {
		return c, fmt.Errorf("period: %w", err)
	}
	if c.Grace, err = time.ParseDuration(f.grace); err != nil {
		return c, fmt.Errorf("grace: %w", err)
	}
	if c.AlertAfter, err = strconv.Atoi(f.alertAfter); err != nil {
		return c, fmt.Errorf("alert after: %w", err)
	}
	if c.RecoverAfter, err = strconv.Atoi(f.recoverAfter); err != nil {
		return c, fmt.Errorf("recover after: %w", err)
	}
	return c, config.ValidateCheck(c)
}

// renderHeartbeatFields renders the rows before Channel for a heartbeat.
func renderHeartbeatFields(b *strings.Builder, f monitorForm) {
	textRow(b, f, 0, "Name", f.values[0])
	marker, label := " ", "Schedule"
	if f.focus == focusSchedule {
		marker, label = accentStyle.Render(">"), accentStyle.Render(label)
	}
	fmt.Fprintf(b, "%s %s: %s\n", marker, label, renderChoice(f.scheduleName(), scheduleChoices, f.focus == focusSchedule))
	if !f.isCron() {
		textRow(b, f, focusPeriod, "Period", f.period)
		textRow(b, f, focusGrace, "Grace", f.grace)
		b.WriteString(helpStyle.Render(heartbeatCopy) + "\n")
		return
	}
	var row strings.Builder
	textRow(&row, f, focusCron, "Cron", f.cron)
	line := strings.TrimSuffix(row.String(), "\n")
	if f.focus == focusCron {
		line += "   " + helpStyle.Render(cronHint)
	}
	b.WriteString(line + "\n")
	textRow(b, f, focusGrace, "Grace", f.grace)
	b.WriteString(helpStyle.Render(cronCopy) + "\n")
}

// renderCronRuns writes the zone cron runs are in and, while the expression
// parses, its next three runs there.
func renderCronRuns(b *strings.Builder, f monitorForm, snap Snapshot, now time.Time) {
	zone := snap.ScheduleZone()
	b.WriteString(helpStyle.Render(fmt.Sprintf("%-*s", cronLabelWidth, "Zone")) + zone.String() + " (Defaults)\n")
	runs := snap.NextRuns(config.Check{Kind: kindHeartbeat, Schedule: config.ScheduleCron, Cron: f.cron}, now, 3)
	if len(runs) == 0 {
		b.WriteString("\n")
		return
	}
	b.WriteString(helpStyle.Render(fmt.Sprintf("%-*s", cronLabelWidth, "Next runs")) + formatRuns(runs, now.In(zone)) + "\n")
}

// formatRuns lists runs as "Mon 26 02:00 · Tue 27 02:00", in their own
// location, with the month when it is not now's.
func formatRuns(runs []time.Time, now time.Time) string {
	out := make([]string, len(runs))
	for i, run := range runs {
		layout := "Mon 2 15:04"
		if run.Year() != now.Year() || run.Month() != now.Month() {
			layout = "Mon Jan 2 15:04"
		}
		out[i] = run.Format(layout)
	}
	return strings.Join(out, " · ")
}

// focusCronError moves the focus to the Cron row when a save error is about
// the expression.
func (f *monitorForm) focusCronError(err error) {
	for _, line := range strings.Split(err.Error(), "\n") {
		if strings.HasPrefix(line, "cron ") {
			f.focus, f.cursor = focusCron, len([]rune(f.cron))
			return
		}
	}
}

// firstAlertHint says how long after the last ping the first alert comes:
// each missed period + grace counts as one failure.
func firstAlertHint(f monitorForm) string {
	n, err := strconv.Atoi(f.alertAfter)
	period, perr := time.ParseDuration(f.period)
	grace, gerr := time.ParseDuration(f.grace)
	if err != nil || perr != nil || gerr != nil || n < 1 || period <= 0 || grace < 0 {
		return ""
	}
	return "First alert ≈ " + shortDuration(time.Duration(n)*(period+grace)) + " after the last ping"
}

// renderTokenConfirm is the question that replaces the key help after
// Token ‹ new › and enter.
func renderTokenConfirm(b *strings.Builder) {
	b.WriteString("\n" + warnStyle.Render("Replace the token? The current URL stops working now;") + "\n")
	b.WriteString(warnStyle.Render("jobs that still use it look missed until updated.") + "\n")
	b.WriteString("\n" + renderHelp("y replace · n keep the token"))
}

// formVariants are the form as every Type and Schedule would show it, and
// with the token question, so the box can keep one size whichever is on
// screen.
func formVariants(f monitorForm) []monitorForm {
	var out []monitorForm
	for _, kind := range []string{"", kindTCP, kindHeartbeat} {
		v := f
		v.confirm, v.cursor = false, 0
		v.setKind(kind)
		schedules := []string{v.schedule}
		if v.isHeartbeat() {
			schedules = []string{"", config.ScheduleCron}
		}
		for _, schedule := range schedules {
			v.schedule, v.confirm = schedule, false
			out = append(out, v)
			if v.tokenRow() {
				v.confirm = true
				out = append(out, v)
			}
		}
	}
	return out
}

// padFormHeight adds blank lines above the key help so the form screen has
// height lines.
func padFormHeight(screen string, height int) string {
	lines := strings.Split(screen, "\n")
	if len(lines) >= height || len(lines) < 2 {
		return screen
	}
	at := len(lines) - 2
	padded := append(append(append([]string{}, lines[:at]...), make([]string, height-len(lines))...), lines[at:]...)
	return strings.Join(padded, "\n")
}

// hostOnly drops a port from a host name ("srv:443" → "srv").
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// listenerPingBase is the ping URL without its token from the daemon's
// listener, or only the path when it has none.
func listenerPingBase(snap Snapshot, host string) string {
	return snap.API.BaseURL(host) + config.HeartbeatPath("")
}

// renderPingURLScreen is the form box after a heartbeat is saved: the full
// ping URL, plain on its own line so a mouse selection copies only it. When
// the terminal is too narrow for the line, the base and the token get a line
// each, neither of them broken.
func renderPingURLScreen(f monitorForm, snap Snapshot, base string, width int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Ping URL · "+f.values[0]) + "\n")
	if f.replaced {
		b.WriteString("Saved. The old URL no longer works. The new one:\n\n")
	} else {
		b.WriteString("Saved. The job sends a POST to this URL when it ends:\n\n")
	}
	if full := "URL    " + base + f.pingToken; width <= 0 || len(full)+6 <= width {
		b.WriteString(full + "\n")
	} else {
		b.WriteString("URL    " + base + "\n")
		b.WriteString("Token  " + f.pingToken + "\n\n")
		b.WriteString(helpStyle.Render("Join them for the full URL.") + "\n")
	}
	b.WriteString("\n" + helpStyle.Render("Add /fail to the URL when the job fails.") + "\n")
	b.WriteString(helpStyle.Render("Shown in full only now; v in the detail shows it again.") + "\n")
	if !snap.API.Enabled() {
		b.WriteString(warnStyle.Render(noListenerLine) + "\n")
	}
	b.WriteString("\n" + renderHelp("esc close"))
	return b.String()
}
