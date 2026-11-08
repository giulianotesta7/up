package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

// The tabs of the notifications view, in the order tab cycles them.
const (
	tabMail = iota
	tabChannels
	tabDeliveries
)

var notifyTabs = []string{"Mail servers", "Channels", "Deliveries"}

// shortTimeLayout is the time of the delivery lists: no seconds.
const shortTimeLayout = "Jan 02 15:04"

const (
	mailHelp           = "↑/↓ select · n new · e edit · d delete · tab next · esc back · q quit"
	channelsHelp       = "↑/↓ select · enter deliveries · n new · e edit · d delete · t test · tab next · esc back · q quit"
	channelsHelpShort  = "↑/↓ · enter deliveries · n e d t · tab next · esc back · q quit"
	deliveriesHelp     = "↑/↓ scroll · / search · tab next · esc back · q quit"
	noDeliveryMatch    = "No deliveries match. esc clears."
	noDeliveries       = "No deliveries yet."
	noDeliveryChannel  = "No deliveries yet. Add a channel on the Channels tab."
	noDeliveryMonitors = "No deliveries yet. Choose a channel when editing a monitor."
)

// deliveryFilter narrows the Deliveries tab: a case-insensitive query on
// monitor, channel, event, outcome and reason, and the channel chosen with
// enter on the Channels tab. open is the search bar being edited.
type deliveryFilter struct {
	query, channel string
	open           bool
}

func (f deliveryFilter) active() bool { return f.open || f.query != "" || f.channel != "" }

func (f deliveryFilter) matches(a store.Attempt) bool {
	if f.channel != "" && a.Channel != f.channel {
		return false
	}
	if f.query == "" {
		return true
	}
	query := strings.ToLower(f.query)
	for _, text := range []string{a.Monitor, a.Channel, string(a.Event), string(a.Outcome), a.Reason} {
		if strings.Contains(strings.ToLower(text), query) {
			return true
		}
	}
	return false
}

// openNotifications shows the notifications view on tab.
func (m Model) openNotifications(tab int) Model {
	m.notify, m.notifyTab = true, tab
	m.smtpSelected, m.channelSelected = 0, 0
	m.deliveries, m.deliverySelected, m.deliveryTop = deliveryFilter{}, 0, 0
	m.notice, m.noticeOK = "", false
	return m
}

func (m Model) handleNotifyKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.smtpForm != nil || m.smtpDelete != nil {
		return m.handleSMTPKey(msg)
	}
	if m.channelForm != nil || m.channelDelete != nil || m.channelTest != nil {
		return m.handleChannelKey(msg)
	}
	if m.deliveries.open {
		return m.handleDeliverySearchKey(msg)
	}
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "r":
		if m.destination != "" {
			m.generation++
			return m, m.load
		}
		return m, nil
	case "esc":
		if m.notifyTab == tabDeliveries && m.deliveries.active() {
			m.deliveries, m.deliverySelected, m.deliveryTop = deliveryFilter{}, 0, 0
		} else {
			m.notify = false
		}
		return m, nil
	case "tab":
		m.notifyTab = (m.notifyTab + 1) % len(notifyTabs)
		return m, nil
	case "shift+tab":
		m.notifyTab = (m.notifyTab + len(notifyTabs) - 1) % len(notifyTabs)
		return m, nil
	}
	switch m.notifyTab {
	case tabMail:
		return m.handleSMTPKey(msg)
	case tabChannels:
		if msg.String() == "enter" {
			if m.channelSelected < len(m.snap.Channels) {
				m.notifyTab = tabDeliveries
				m.deliveries = deliveryFilter{channel: m.snap.Channels[m.channelSelected].Name}
				m.deliverySelected, m.deliveryTop = 0, 0
			}
			return m, nil
		}
		return m.handleChannelKey(msg)
	}
	switch msg.String() {
	case "/":
		if len(m.snap.Attempts) > 0 {
			m.deliveries.open = true
		}
	case "up", "k":
		m = m.moveDelivery(-1)
	case "down", "j":
		m = m.moveDelivery(1)
	}
	return m, nil
}

// handleDeliverySearchKey edits the open search bar of the Deliveries tab.
func (m Model) handleDeliverySearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.deliveries = deliveryFilter{}
	case "enter":
		m.deliveries.open = false
		return m, nil
	case "up":
		return m.moveDelivery(-1), nil
	case "down":
		return m.moveDelivery(1), nil
	case "backspace":
		runes := []rune(m.deliveries.query)
		if len(runes) == 0 {
			return m, nil
		}
		m.deliveries.query = string(runes[:len(runes)-1])
	default:
		if msg.Text == "" {
			return m, nil
		}
		m.deliveries.query += msg.Text
	}
	m.deliverySelected, m.deliveryTop = 0, 0
	return m, nil
}

// pasteDeliverySearch adds pasted text to the open search bar.
func (m Model) pasteDeliverySearch(text string) Model {
	m.deliveries.query += strings.NewReplacer("\r", "", "\n", " ").Replace(text)
	m.deliverySelected, m.deliveryTop = 0, 0
	return m
}

// shownDeliveries are the attempts the filter lets through, newest first.
func (m Model) shownDeliveries() []store.Attempt {
	if !m.deliveries.active() {
		return m.snap.Attempts
	}
	var out []store.Attempt
	for _, a := range m.snap.Attempts {
		if m.deliveries.matches(a) {
			out = append(out, a)
		}
	}
	return out
}

// notifyTop is the part of the view above the table: the tabs, a blank line
// and the banners.
func (m Model) notifyTop(now time.Time) []string {
	top := []string{m.tabsLine(), ""}
	if banners := strings.TrimSuffix(renderBanners(m.snap, now, m.err), "\n"); banners != "" {
		top = append(top, strings.Split(banners, "\n")...)
	}
	return top
}

// deliveryPage is how many delivery rows fit; 0 means all (unknown height).
func (m Model) deliveryPage(now time.Time) int {
	if m.height <= 0 {
		return 0
	}
	// Header, rule, table header, position line, blank, key help and footer.
	return max(1, m.height-7-len(m.notifyTop(now)))
}

// clampDeliveries keeps the selection on a shown row and inside the page.
func (m Model) clampDeliveries(shown, page int) Model {
	m.deliverySelected = max(0, min(m.deliverySelected, shown-1))
	if page <= 0 {
		m.deliveryTop = 0
		return m
	}
	m.deliveryTop = max(0, min(m.deliveryTop, shown-page))
	if m.deliverySelected < m.deliveryTop {
		m.deliveryTop = m.deliverySelected
	}
	if m.deliverySelected >= m.deliveryTop+page {
		m.deliveryTop = m.deliverySelected - page + 1
	}
	return m
}

func (m Model) moveDelivery(delta int) Model {
	m.deliverySelected += delta
	return m.clampDeliveries(len(m.shownDeliveries()), m.deliveryPage(m.now()))
}

// tabsLine names the tabs, the current one reversed, and the kept delivery
// filter after them.
func (m Model) tabsLine() string {
	tabs := make([]string, len(notifyTabs))
	for i, tab := range notifyTabs {
		if i == m.notifyTab {
			tabs[i] = accentStyle.Reverse(true).Render(" " + tab + " ")
		} else {
			tabs[i] = helpStyle.Render(" " + tab + " ")
		}
	}
	line := "  " + strings.Join(tabs, "  ")
	if m.notifyTab != tabDeliveries || !m.deliveries.active() {
		return line
	}
	var parts []string
	if m.deliveries.channel != "" {
		parts = append(parts, "channel "+displayText(m.deliveries.channel))
	}
	if !m.deliveries.open {
		if m.deliveries.query != "" {
			parts = append(parts, displayText(m.deliveries.query))
		}
		parts = append(parts, fmt.Sprintf("%d of %d", len(m.shownDeliveries()), len(m.snap.Attempts)), "esc clears")
	}
	if len(parts) == 0 {
		return line
	}
	return line + "   " + helpStyle.Render(strings.Join(parts, " · "))
}

// renderNotifications is the notifications view below the header and rule.
// With a known height it fills the screen, the key help on the last line
// above the footer, so nothing moves when tabs switch or a search narrows.
func (m Model) renderNotifications(now time.Time) string {
	lines := m.notifyTop(now)
	var help string
	switch m.notifyTab {
	case tabMail:
		lines = append(lines, renderSMTPTable(m.snap, m.smtpSelected)...)
		help = renderHelp(mailHelp)
	case tabChannels:
		lines = append(lines, renderChannelTable(m.snap, m.channelSelected, now, m.width)...)
		help = renderHelp(channelsHelp)
		if m.width > 0 && ansi.StringWidth(channelsHelp) > m.width {
			help = renderHelp(channelsHelpShort)
		}
	default:
		lines = append(lines, m.renderDeliveries(now)...)
		help = renderHelp(deliveriesHelp)
		if m.deliveries.open {
			help = m.deliveryPrompt()
		}
	}
	if m.height > 0 {
		room := max(1, m.height-3) - 1
		if len(lines) > room {
			lines = lines[:room]
		}
		for len(lines) < room {
			lines = append(lines, "")
		}
	} else {
		lines = append(lines, "")
	}
	lines = append(lines, help)
	if m.width > 0 {
		for i, line := range lines {
			lines[i] = ansi.Truncate(line, m.width, "")
		}
	}
	return strings.Join(lines, "\n")
}

// deliveryPrompt is the footer while the search bar is open. A narrow line
// drops the key hints, then shortens the counter, then the head of the query.
func (m Model) deliveryPrompt() string {
	build := func(query, counter string) string {
		return accentStyle.Render("/") + " " + displayText(query) + accentStyle.Render("█") + "   " + helpStyle.Render(counter)
	}
	shown, total := len(m.shownDeliveries()), len(m.snap.Attempts)
	long := fmt.Sprintf("%d of %d", shown, total)
	for _, counter := range []string{long + " · enter keep · esc clear", long, fmt.Sprintf("%d/%d", shown, total)} {
		if line := build(m.deliveries.query, counter); m.width <= 0 || ansi.StringWidth(line) <= m.width {
			return line
		}
	}
	counter := fmt.Sprintf("%d/%d", shown, total)
	query := displayText(m.deliveries.query)
	room := max(0, m.width-ansi.StringWidth(build("", counter))-1)
	return build("…"+ansi.TruncateLeft(query, ansi.StringWidth(query)-room, ""), counter)
}

// renderSMTPTable is the Mail servers tab.
func renderSMTPTable(snap Snapshot, selected int) []string {
	if len(snap.SMTPServers) == 0 {
		return []string{helpStyle.Render("No mail servers configured.")}
	}
	widths := [5]int{4, 4, 4, 3, 4}
	rows := make([][5]string, len(snap.SMTPServers))
	for i, s := range snap.SMTPServers {
		rows[i] = [5]string{s.Name, s.Host, strconv.Itoa(s.Port), string(s.TLS), orDash(s.Username)}
		for j, v := range rows[i] {
			widths[j] = max(widths[j], ansi.StringWidth(v))
		}
	}
	line := func(marker string, cols [5]string, last string) string {
		var out strings.Builder
		out.WriteString(marker + "   ")
		for j, v := range cols {
			out.WriteString(v)
			out.WriteString(strings.Repeat(" ", widths[j]-ansi.StringWidth(v)+2))
		}
		out.WriteString(last)
		return out.String()
	}
	lines := []string{headerStyle.Render(line(" ", [5]string{"NAME", "HOST", "PORT", "TLS", "USER"}, "PASSWORD"))}
	for i, s := range snap.SMTPServers {
		marker := " "
		cols := rows[i]
		if i == selected {
			marker = accentStyle.Render("▌")
			cols[0] = selectedNameStyle.Render(cols[0])
		}
		lines = append(lines, line(marker, cols, smtpPasswordState(snap, s)))
	}
	return lines
}

// renderChannelTable is the Channels tab: one row per channel with where it
// sends and its last delivery, and the selected channel described under the
// table. A narrow screen drops the SENDS TO column; the description keeps it.
func renderChannelTable(snap Snapshot, selected int, now time.Time, width int) []string {
	if len(snap.Channels) == 0 {
		return []string{helpStyle.Render("No channels configured.")}
	}
	widths := [3]int{4, 4, len("SENDS TO")}
	rows := make([][3]string, len(snap.Channels))
	counts := make([]int, len(rows))
	last := make([]string, len(rows))
	countWidth, lastWidth := len("MONITORS"), len("LAST DELIVERY")
	for i, c := range snap.Channels {
		rows[i] = [3]string{c.Name, c.Kind, c.SendsTo()}
		for j, v := range rows[i] {
			widths[j] = max(widths[j], ansi.StringWidth(v))
		}
		for _, s := range snap.Services {
			if s.Check.Channel == c.Name {
				counts[i]++
			}
		}
		countWidth = max(countWidth, len(strconv.Itoa(counts[i])))
		last[i] = lastDelivery(snap, c.Name, now)
		lastWidth = max(lastWidth, ansi.StringWidth(last[i]))
	}
	columns, countHeader := 3, "MONITORS"
	full := 4 + widths[0] + widths[1] + widths[2] + 6 + countWidth + 2 + lastWidth
	if width > 0 && full > width {
		columns, countHeader = 2, "MON"
		countWidth = len(countHeader)
		for _, n := range counts {
			countWidth = max(countWidth, len(strconv.Itoa(n)))
		}
	}
	line := func(marker string, cols [3]string, count, delivery string) string {
		var out strings.Builder
		out.WriteString(marker + "   ")
		for i, v := range cols[:columns] {
			out.WriteString(v)
			out.WriteString(strings.Repeat(" ", widths[i]-ansi.StringWidth(v)+2))
		}
		out.WriteString(strings.Repeat(" ", countWidth-ansi.StringWidth(count)) + count + "  " + delivery)
		return strings.TrimRight(out.String(), " ")
	}
	lines := []string{headerStyle.Render(line(" ", [3]string{"NAME", "KIND", "SENDS TO"}, countHeader, "LAST DELIVERY"))}
	for i, row := range rows {
		marker := " "
		if i == selected {
			marker = accentStyle.Render("▌")
			row[0] = selectedNameStyle.Render(row[0])
		}
		lines = append(lines, line(marker, row, strconv.Itoa(counts[i]), last[i]))
	}
	if selected < len(snap.Channels) {
		c := snap.Channels[selected]
		lines = append(lines, "", "  "+selectedNameStyle.Render(c.Name)+"  "+helpStyle.Render(channelLine(c)))
	}
	return lines
}

// channelLine describes where a channel delivers, never a secret: the mail
// server and first recipient, the webhook origin and its authentication, or
// a push service's target and options.
func channelLine(c config.Channel) string {
	parts := []string{c.Kind}
	priority := func(def string) string {
		if c.Priority == "" {
			return "priority " + def
		}
		return "priority " + c.Priority
	}
	switch c.Kind {
	case config.KindWebhook:
		auth := "no auth"
		if c.WebhookAuthName != "" {
			auth = c.WebhookAuthName
		}
		parts = append(parts, c.WebhookOrigin, auth)
	case config.KindNtfy:
		token := "no access token"
		if !c.Token.Empty() {
			token = "access token"
		}
		parts = append(parts, c.ServerURL, "topic "+c.Topic, priority(config.DefaultNtfyPriority), token)
	case config.KindGotify:
		parts = append(parts, c.ServerURL, priority(config.DefaultGotifyPriority))
	default:
		parts = append(parts, c.SendsTo())
	}
	return strings.Join(parts, " · ")
}

// lastDelivery is the Channels tab cell: the outcome, time and reason of the
// channel's newest delivery, or a dash.
func lastDelivery(snap Snapshot, channel string, now time.Time) string {
	for _, a := range snap.LastAttempts {
		if a.Channel != channel {
			continue
		}
		text := outcomeLabel(a.Outcome) + " " + shortTime(a.At, now)
		if a.Reason != "" {
			text += " · " + displayText(a.Reason)
		}
		return outcomeStyle(a.Outcome).Render(text)
	}
	return helpStyle.Render("—")
}

func outcomeLabel(o store.Outcome) string {
	switch o {
	case store.OutcomeSent:
		return "✓ sent"
	case store.OutcomeFailed:
		return "✗ failed"
	case store.OutcomeHeld:
		return "◷ held"
	}
	return displayText(string(o))
}

func outcomeStyle(o store.Outcome) lipgloss.Style {
	switch o {
	case store.OutcomeSent:
		return upStyle
	case store.OutcomeFailed:
		return downStyle
	}
	return warnStyle
}

// shortTime is a time in the delivery lists: no seconds.
func shortTime(t, now time.Time) string {
	return t.In(now.Location()).Format(shortTimeLayout)
}

// shortEvent names an event in a narrow Deliveries table.
func shortEvent(e store.Event) string {
	switch e {
	case store.EventDown:
		return "down"
	case store.EventRecovered:
		return "recovered"
	case store.EventCert:
		return "cert"
	}
	return displayText(string(e))
}

// deliveryEmpty is the Deliveries tab with no attempt at all, with the next
// step when one is missing.
func deliveryEmpty(snap Snapshot) string {
	if len(snap.Channels) == 0 {
		return noDeliveryChannel
	}
	for _, s := range snap.Services {
		if s.Check.Channel != "" {
			return noDeliveries
		}
	}
	return noDeliveryMonitors
}

// renderDeliveries is the Deliveries tab: the table, newest first, from the
// scroll position, and the position under it. Column widths come from every
// loaded attempt, so filtering never moves the table header.
func (m Model) renderDeliveries(now time.Time) []string {
	if len(m.snap.Attempts) == 0 {
		return []string{helpStyle.Render(deliveryEmpty(m.snap))}
	}
	monitorWidth, channelWidth, eventWidth, reasonWidth := len("MONITOR"), len("CHANNEL"), len("EVENT"), len("REASON")
	shortWidth := eventWidth
	for _, a := range m.snap.Attempts {
		monitorWidth = max(monitorWidth, ansi.StringWidth(displayText(a.Monitor)))
		channelWidth = max(channelWidth, ansi.StringWidth(displayText(a.Channel)))
		eventWidth = max(eventWidth, ansi.StringWidth(displayText(string(a.Event))))
		shortWidth = max(shortWidth, ansi.StringWidth(shortEvent(a.Event)))
		reasonWidth = max(reasonWidth, ansi.StringWidth(displayText(a.Reason)))
	}
	const timeWidth, outcomeWidth = len(shortTimeLayout), 8
	compact := m.width > 0 && 4+timeWidth+monitorWidth+channelWidth+eventWidth+outcomeWidth+10+reasonWidth > m.width
	if compact {
		monitorWidth, channelWidth, eventWidth = min(monitorWidth, 10), min(channelWidth, 10), shortWidth
	}
	cell := func(text string, width int) string {
		if ansi.StringWidth(text) > width {
			text = ansi.Truncate(text, width, "…")
		}
		return text + strings.Repeat(" ", width-ansi.StringWidth(text))
	}
	header := "    " + cell("TIME", timeWidth) + "  " + cell("MONITOR", monitorWidth) + "  " + cell("CHANNEL", channelWidth) + "  " + cell("EVENT", eventWidth) + "  " + cell("OUTCOME", outcomeWidth) + "  REASON"
	lines := []string{headerStyle.Render(header)}
	shown := m.shownDeliveries()
	if len(shown) == 0 {
		return append(lines, "  "+noDeliveryMatch)
	}
	page := m.deliveryPage(now)
	view := m.clampDeliveries(len(shown), page)
	end := len(shown)
	if page > 0 {
		end = min(end, view.deliveryTop+page)
	}
	for i := view.deliveryTop; i < end; i++ {
		a := shown[i]
		marker, monitor := " ", cell(displayText(a.Monitor), monitorWidth)
		if i == view.deliverySelected {
			marker, monitor = accentStyle.Render("▌"), selectedNameStyle.Render(monitor)
		}
		outcome := outcomeStyle(a.Outcome).Render(outcomeLabel(a.Outcome))
		event := displayText(string(a.Event))
		if compact {
			event = shortEvent(a.Event)
		}
		row := "  " + marker + " " + shortTime(a.At, now) + "  " + monitor + "  " + cell(displayText(a.Channel), channelWidth) + "  " + cell(event, eventWidth) + "  " +
			outcome + strings.Repeat(" ", outcomeWidth-ansi.StringWidth(outcomeLabel(a.Outcome))) + "  " + displayText(a.Reason)
		if m.width > 0 && ansi.StringWidth(row) > m.width {
			row = ansi.Truncate(row, m.width, "…")
		}
		lines = append(lines, strings.TrimRight(row, " "))
	}
	position := fmt.Sprintf("  %d–%d of %d", view.deliveryTop+1, end, len(shown))
	switch {
	case view.deliveryTop > 0 && end < len(shown):
		position += " ↑↓"
	case end < len(shown):
		position += " ↓"
	case view.deliveryTop > 0:
		position += " ↑"
	}
	return append(lines, helpStyle.Render(position))
}
