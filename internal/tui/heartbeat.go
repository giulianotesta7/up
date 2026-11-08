package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

// heartbeatTokens is how this model shows and replaces ping tokens: through
// the store when it can (a remote server), otherwise with the key next to
// the local database. nil when neither is possible.
func (m Model) heartbeatTokens() HeartbeatTokens {
	switch s := m.store.(type) {
	case HeartbeatTokens:
		return s
	case *store.Store:
		return NewLocalHeartbeatTokens(s, m.databasePath)
	}
	return nil
}

// pingView is what the detail needs to show a heartbeat's ping URL: its base
// without the token and, after v, the token.
type pingView struct {
	base, token string
}

// pingBase is the ping URL without its token. Remotely it is the login URL,
// which this machine already reaches the API at (behind a proxy too);
// locally it is the daemon's listener.
func (m Model) pingBase() string {
	if m.destination != "" {
		return strings.TrimRight(m.destination, "/") + config.HeartbeatPath("")
	}
	return listenerPingBase(m.snap, hostOnly(m.hostname))
}

// pingView is the ping URL state of the selected monitor.
func (m Model) pingView() pingView {
	p := pingView{base: m.pingBase()}
	if m.hasSelection() && m.snap.Services[m.selected].Check.Name == m.revealName {
		p.token = m.revealToken
	}
	return p
}

type revealMsg struct {
	name, token string
	err         error
}

// reveal reads the ping token of the heartbeat called name.
func (m Model) reveal(name string) tea.Cmd {
	tokens := m.heartbeatTokens()
	return func() tea.Msg {
		if tokens == nil {
			return revealMsg{name: name, err: errors.New("this server cannot show ping URLs")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		token, err := tokens.RevealHeartbeatToken(ctx, name)
		return revealMsg{name: name, token: token, err: err}
	}
}

// toggleReveal is v: it shows the selected heartbeat's ping URL in the
// detail, or hides it again.
func (m Model) toggleReveal() (Model, tea.Cmd) {
	if !m.hasSelection() || (!m.detail && !m.splitMode()) {
		return m, nil
	}
	c := m.snap.Services[m.selected].Check
	if c.Kind != config.CheckHeartbeat {
		return m, nil
	}
	if m.revealName == c.Name {
		m.revealName, m.revealToken = "", ""
		return m, nil
	}
	return m, m.reveal(c.Name)
}

// handleReveal shows a revealed token while its monitor is still on screen;
// a failure opens a box with the reason and what to do.
func (m Model) handleReveal(msg revealMsg) Model {
	covered := m.form != nil || m.deletion != nil || m.mute != nil || m.maint != nil || m.defaults != nil || m.notify || m.help
	if covered || !m.hasSelection() || m.snap.Services[m.selected].Check.Name != msg.name || (!m.detail && !m.splitMode()) {
		return m
	}
	if msg.err != nil {
		text := msg.err.Error()
		if !errors.Is(msg.err, ErrRevealUnavailable) {
			text = "Cannot show the ping URL: " + text
		}
		m.pingError = &pingErrorBox{name: msg.name, text: text}
		return m
	}
	m.revealName, m.revealToken = msg.name, msg.token
	return m
}

// pingErrorBox explains why a ping URL cannot be shown.
type pingErrorBox struct{ name, text string }

func renderPingError(p pingErrorBox, width int) string {
	wrap := 70
	if width > 0 {
		wrap = min(wrap, max(30, width-6))
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("Ping URL · "+p.name) + "\n")
	for _, line := range strings.Split(ansi.Wrap(p.text, wrap, "/"), "\n") {
		b.WriteString(warnStyle.Render(line) + "\n")
	}
	return b.String() + "\n" + renderHelp("esc close")
}

// handleSaved finishes a save that made a heartbeat token: the form box
// shows the URL, or a notice says how to get one when it could not be made.
func (m Model) handleSaved(msg saveMsg) Model {
	switch {
	case msg.pingToken != "":
		f := *m.form
		f.saving, f.pingToken, f.replaced = false, msg.pingToken, msg.replaced
		m.form = &f
	case msg.tokenErr != nil:
		m.form = nil
		m.notice, m.noticeOK = fmt.Sprintf("Saved %s, but its ping URL cannot be shown: %v. Use Token: new in Edit monitor to get one.", msg.name, msg.tokenErr), false
	default:
		m.form = nil
	}
	return m
}

// heartbeatTarget is the detail title's text after the name.
func heartbeatTarget(c config.Check) string {
	if isCronCheck(c) {
		return fmt.Sprintf("heartbeat · cron %s · grace %s", c.Cron, shortDuration(c.Grace))
	}
	return fmt.Sprintf("heartbeat · every %s · grace %s", shortDuration(c.Period), shortDuration(c.Grace))
}

func isCronCheck(c config.Check) bool { return c.Schedule == config.ScheduleCron }

// heartbeatCells is one result per cell of a heartbeat's trend, newest
// first: per period, or for a cron heartbeat per expected run.
func heartbeatCells(s Service, snap Snapshot) []check.Result {
	return check.RunResults(s.Check, s.Recent, snap.ScheduleZone())
}

// heartbeatClock is clockWithDay for a heartbeat's times; a cron
// heartbeat's are in the Defaults zone with its abbreviation ("02:30 -03").
func heartbeatClock(t time.Time, s Service, snap Snapshot, now time.Time) string {
	if !isCronCheck(s.Check) {
		return clockWithDay(t, now)
	}
	zone := snap.ScheduleZone()
	local := t.In(zone)
	return clockWithDay(local, now.In(zone)) + " " + local.Format("MST")
}

// pingCells is one cell per heartbeat result, newest right, padded to width:
// a ping, or (red) a failure the job reported and a missed deadline. The
// list shows both failures as █; the detail tells a miss apart with ░.
func pingCells(results []check.Result, width int, detail bool) string {
	results = graphWindow(results, width)
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", max(0, width-len(results))))
	for i := len(results) - 1; i >= 0; i-- {
		switch r := results[i]; {
		case r.Status == check.StatusUp:
			b.WriteString(upStyle.Render("▂"))
		case detail && check.Missed(r):
			b.WriteString(downStyle.Render("░"))
		default:
			b.WriteString(downStyle.Render("█"))
		}
	}
	return b.String()
}

// heartbeatLast is the LAST CHECK cell of a heartbeat: its last ping.
func heartbeatLast(s Service, now time.Time) string {
	if s.Heartbeat.LastPing.IsZero() {
		return "—"
	}
	return ago(now, s.Heartbeat.LastPing)
}

// heartbeatReason is the list's last column for a heartbeat.
func heartbeatReason(s Service, snap Snapshot, now time.Time) string {
	due := ""
	if !s.Heartbeat.Deadline.IsZero() {
		due = heartbeatClock(s.Heartbeat.Deadline, s, snap, now)
	}
	switch {
	case !s.HasResult && !snap.API.Enabled():
		return warnStyle.Render("needs --api-listen")
	case !s.HasResult:
		return helpStyle.Render("no ping yet")
	case check.Failed(s.Latest):
		return helpStyle.Render("job sent /fail")
	case check.Missed(s.Latest):
		return helpStyle.Render("missed · due by " + due)
	}
	return helpStyle.Render("next by " + due)
}

// heartbeatStatus writes a heartbeat's STATUS rows up to Last error.
func heartbeatStatus(field func(label, value string), s Service, snap Snapshot, now time.Time) {
	status := styledLabel(s, 0) + "   no ping yet"
	if !s.Heartbeat.LastPing.IsZero() {
		status = styledLabel(s, 0) + "   last ping " + ago(now, s.Heartbeat.LastPing)
	}
	field("Status", status)
	due := "after the first ping"
	if d := s.Heartbeat.Deadline; !d.IsZero() {
		due = "before " + heartbeatClock(d, s, snap, now)
		if d.After(now) {
			due += " (in " + timeLeft(d.Sub(now)) + ")"
		} else {
			due += " (overdue)"
		}
	}
	field("Next due", due)
}

// renderPings is the PINGS section, which takes the latency chart's place:
// one cell per period, or per expected run of a cron heartbeat.
func renderPings(b *strings.Builder, s Service, snap Snapshot, width int, opts detailOptions, blank string) {
	unit := "period"
	if isCronCheck(s.Check) {
		unit = "run"
	}
	b.WriteString(blank + headerStyle.Render("PINGS") + "  " + helpStyle.Render("one cell per "+unit+", newest right") + "\n")
	if len(s.Recent) == 0 {
		b.WriteString("  No pings yet.\n")
		return
	}
	cells := recentLimit
	if width > 0 {
		padding := 0
		if opts.compact {
			padding = 2
		}
		cells = min(cells, max(8, width-2-padding))
	}
	window := graphWindow(heartbeatCells(s, snap), cells)
	b.WriteString("  " + pingCells(window, cells, true) + "\n")
	b.WriteString(helpStyle.Render("  older"+strings.Repeat(" ", max(1, cells-8))+"now") + "\n")
	if opts.hideStats {
		return
	}
	var missed, failed int
	for _, r := range window {
		if check.Missed(r) {
			missed++
		} else if r.Status != check.StatusUp {
			failed++
		}
	}
	fmt.Fprintf(b, "%s  %s · %d on time · %s · %s\n", blank, plural(len(window), unit), len(window)-missed-failed,
		downStyle.Render(fmt.Sprintf("░ %d missed", missed)), downStyle.Render(fmt.Sprintf("█ %d failed", failed)))
}

// renderPingURL is the PING URL section: the URL masked until v shows the
// token in place. It takes the same lines either way: one when the whole URL
// fits, otherwise the base and the token on a line each, never broken.
func renderPingURL(b *strings.Builder, field func(label, value string), snap Snapshot, width int, p pingView, blank string) {
	hint := helpStyle.Render("v show")
	if p.token != "" {
		hint = helpStyle.Render("v hide")
	}
	if !snap.API.Enabled() {
		hint = warnStyle.Render("needs --api-listen")
	}
	b.WriteString(blank + headerStyle.Render("PING URL") + "  " + hint + "\n")
	base, token := p.base, pingMask
	if p.token != "" {
		token = p.token
	}
	const label = 15 // "  URL          "
	switch {
	case width <= 0 || label+len(base)+pingTokenWidth <= width:
		field("URL", base+token)
	case label+pingTokenWidth <= width:
		field("URL", base)
		field("Token", token)
	default:
		field("URL", base)
		if p.token != "" {
			token = helpStyle.Render("too narrow; widen the terminal")
		}
		field("Token", token)
	}
}

// heartbeatHelp is the detail's key help for a heartbeat.
func heartbeatHelp(help string, scroll bool, p pingView) string {
	switch {
	case scroll:
		return "↑/↓ incidents · v URL · " + help
	case p.token != "":
		return "v hide URL · " + help
	}
	return "v show URL · " + help
}
