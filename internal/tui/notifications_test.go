package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

const (
	mailFooter       = "↑/↓ select · n new · e edit · d delete · tab next · esc back · q quit"
	channelsFooter   = "↑/↓ select · enter deliveries · n new · e edit · d delete · t test · tab next · esc back · q quit"
	deliveriesFooter = "↑/↓ scroll · / search · tab next · esc back · q quit"
	tabsRow          = "Mail servers    Channels    Deliveries"
)

// notifyStore is a real database with a mail server, four channels (quiet
// has no delivery), three monitors and six deliveries.
func notifyStore(t *testing.T) (*store.Store, Model) {
	t.Helper()
	s, m := formStore(t)
	ctx := context.Background()
	if err := s.Beat(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "mail-main", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ops-mail", "pager", "finance", "quiet"} {
		if err := s.AddChannel(ctx, config.Channel{Name: name, Kind: config.KindEmail, SMTPServer: "mail-main", Recipients: []string{name + "@acme.io"}}); err != nil {
			t.Fatal(err)
		}
	}
	for name, channel := range map[string]string{"payouts": "ops-mail", "backups": "pager", "checkout": "finance"} {
		c := testChecks[0]
		c.Name, c.Channel = name, channel
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range notifyAttempts() {
		if err := s.RecordAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := m.Update(m.Init()())
	return s, next.(Model)
}

func notifyAttempts() []store.Attempt {
	day := func(d, h, min, sec int) time.Time { return time.Date(2026, 10, d, h, min, sec, 0, time.UTC) }
	return []store.Attempt{
		{At: day(24, 22, 10, 0), Monitor: "payouts", Channel: "ops-mail", Event: store.EventDown, Outcome: store.OutcomeSent},
		{At: day(25, 2, 12, 0), Monitor: "checkout", Channel: "finance", Event: store.EventCert, Outcome: store.OutcomeSent},
		{At: day(25, 2, 58, 0), Monitor: "checkout", Channel: "finance", Event: store.EventDown, Outcome: store.OutcomeHeld, Reason: "maintenance window"},
		{At: day(25, 3, 2, 0), Monitor: "backups", Channel: "pager", Event: store.EventRecovered, Outcome: store.OutcomeSent},
		{At: day(25, 3, 4, 0), Monitor: "payouts", Channel: "ops-mail", Event: store.EventDown, Outcome: store.OutcomeSent},
		{At: day(25, 3, 4, 30), Monitor: "backups", Channel: "pager", Event: store.EventDown, Outcome: store.OutcomeFailed, Reason: "status 502"},
	}
}

func screenLines(m Model) []string { return strings.Split(view(m), "\n") }

// rowOf is the index of the first line containing want, or -1.
func rowOf(lines []string, want string) int {
	for i, line := range lines {
		if strings.Contains(line, want) {
			return i
		}
	}
	return -1
}

// deliveryRows are the table rows of the Deliveries tab, top to bottom.
func deliveryRows(m Model) []string {
	var rows []string
	lines := screenLines(m)
	header := rowOf(lines, "OUTCOME")
	for _, line := range lines[header+1:] {
		trimmed := strings.TrimLeft(line, " ▌")
		if !strings.HasPrefix(trimmed, "Oct ") {
			break
		}
		rows = append(rows, line)
	}
	return rows
}

func requireNoText(t *testing.T, m Model, unwanted ...string) {
	t.Helper()
	for _, text := range unwanted {
		if strings.Contains(view(m), text) {
			t.Fatalf("unexpected %q:\n%s", text, view(m))
		}
	}
}

func requireFooter(t *testing.T, m Model, footer string) {
	t.Helper()
	lines := screenLines(m)
	if len(lines) < 2 || strings.TrimRight(lines[len(lines)-2], " ") != footer {
		t.Fatalf("footer = %q, want %q:\n%s", lines[len(lines)-2], footer, view(m))
	}
}

func TestNotificationsViewOpensOnTheKeysTabAndSwitchesTabs(t *testing.T) {
	_, m := notifyStore(t)
	m = sized(m, 100, 30)
	list := view(m)
	listLines := screenLines(m)
	tabs, header := -1, -1
	for _, step := range []struct {
		name    string
		keys    []tea.KeyPressMsg
		columns []string
		footer  string
	}{
		{"s", []tea.KeyPressMsg{keyS}, []string{"NAME", "HOST", "PORT", "TLS", "USER", "PASSWORD", "mail-main"}, mailFooter},
		{"tab", []tea.KeyPressMsg{keyTab}, []string{"NAME", "KIND", "MON", "LAST DELIVERY", "ops-mail"}, channelsFooter},
		{"tab", []tea.KeyPressMsg{keyTab}, []string{"TIME", "MONITOR", "CHANNEL", "EVENT", "OUTCOME", "REASON"}, deliveriesFooter},
		{"tab wraps", []tea.KeyPressMsg{keyTab}, []string{"HOST", "mail-main"}, mailFooter},
		{"shift+tab wraps", []tea.KeyPressMsg{keyShiftTab}, []string{"OUTCOME", "REASON"}, deliveriesFooter},
		{"shift+tab", []tea.KeyPressMsg{keyShiftTab}, []string{"LAST DELIVERY"}, channelsFooter},
		{"esc", []tea.KeyPressMsg{keyEsc}, nil, ""},
		{"c", []tea.KeyPressMsg{keyC}, []string{"LAST DELIVERY", "ops-mail"}, channelsFooter},
	} {
		m, _ = press(t, m, step.keys...)
		lines := screenLines(m)
		if step.footer == "" {
			if view(m) != list {
				t.Fatalf("%s: esc did not return to the monitor list:\n%s", step.name, view(m))
			}
			continue
		}
		requireText(t, m, step.columns...)
		requireFooter(t, m, step.footer)
		if len(lines) != 30 || lines[0] != listLines[0] || lines[1] != listLines[1] {
			t.Fatalf("%s: header or rule moved (%d lines):\n%s", step.name, len(lines), view(m))
		}
		if !strings.HasSuffix(view(m), "\ndatabase: ") {
			t.Fatalf("%s: database footer lost:\n%s", step.name, view(m))
		}
		row := rowOf(lines, tabsRow)
		if tabs == -1 {
			tabs = row
			header = rowOf(lines, "NAME")
		}
		tableHeader := rowOf(lines, step.columns[0])
		if strings.Contains(step.footer, "scroll") {
			tableHeader = rowOf(lines, "OUTCOME")
		}
		if row != 2 || row != tabs || tableHeader != header {
			t.Fatalf("%s: tabs row %d (want 2), table header %d (want %d):\n%s", step.name, row, tableHeader, header, view(m))
		}
	}
}

func TestNotificationsEscReturnsToTheScreenItOpenedFrom(t *testing.T) {
	_, m := notifyStore(t)
	m = sized(m, 100, 30)
	m, _ = press(t, m, keyEnter)
	detail := view(m)
	for _, open := range []tea.KeyPressMsg{keyS, keyC} {
		opened, _ := press(t, m, open)
		requireText(t, opened, tabsRow)
		closed, _ := press(t, opened, keyEsc)
		if view(closed) != detail {
			t.Fatalf("esc after %s did not return to the detail:\n%s", open.Text, view(closed))
		}
	}
}

func TestChannelsTabShowsTheLastDeliveryOfEachChannel(t *testing.T) {
	_, base := notifyStore(t)
	for _, size := range [][2]int{{0, 0}, {100, 30}, {80, 24}} {
		m, _ := press(t, sized(base, size[0], size[1]), keyC)
		out := view(m)
		for channel, want := range map[string]string{
			"ops-mail": "✓ sent Oct 25 03:04",
			"pager":    "✗ failed Oct 25 03:04 · status 502",
			"finance":  "◷ held Oct 25 02:58 · maintenance window",
			"quiet":    "—",
		} {
			line := lineWith(out, channel)
			if !strings.HasSuffix(strings.TrimRight(line, " "), want) {
				t.Errorf("%dx%d: %s row = %q, want it to end with %q", size[0], size[1], channel, line, want)
			}
		}
		counts := map[string]string{"ops-mail": "1", "pager": "1", "finance": "1", "quiet": "0"}
		for channel, count := range counts {
			if line := lineWith(out, channel); !strings.Contains(line, " "+count+"  ") {
				t.Errorf("%dx%d: %s monitors = %q, want %s", size[0], size[1], channel, line, count)
			}
		}
		if strings.Contains(out, "03:04:30") {
			t.Errorf("%dx%d: times must drop seconds:\n%s", size[0], size[1], out)
		}
	}
}

func TestDeliveriesTabListsTheLogNewestFirst(t *testing.T) {
	_, m := notifyStore(t)
	m, _ = press(t, sized(m, 100, 30), keyS, keyShiftTab)
	rows := deliveryRows(m)
	want := [][]string{
		{"Oct 25 03:04", "backups", "pager", "monitor.down", "✗ failed", "status 502"},
		{"Oct 25 03:04", "payouts", "ops-mail", "monitor.down", "✓ sent"},
		{"Oct 25 03:02", "backups", "pager", "monitor.recovered", "✓ sent"},
		{"Oct 25 02:58", "checkout", "finance", "monitor.down", "◷ held", "maintenance window"},
		{"Oct 25 02:12", "checkout", "finance", "cert.expiring", "✓ sent"},
		{"Oct 24 22:10", "payouts", "ops-mail", "monitor.down", "✓ sent"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d:\n%s", len(rows), len(want), view(m))
	}
	for i, cells := range want {
		at := 0
		for _, cell := range cells {
			next := strings.Index(rows[i][at:], cell)
			if next < 0 {
				t.Fatalf("row %d = %q, want %q in order", i, rows[i], cells)
			}
			at += next + len(cell)
		}
	}
	if !strings.HasPrefix(rows[0], "  ▌ ") || strings.Contains(rows[1], "▌") {
		t.Errorf("the first row must be selected:\n%s", view(m))
	}
	if strings.Contains(view(m), "03:04:30") {
		t.Errorf("times must drop seconds:\n%s", view(m))
	}
}

func TestDeliveriesScrollKeepsTheHeaderInPlace(t *testing.T) {
	s, m := notifyStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	for i := range 34 {
		if err := s.RecordAttempt(ctx, store.Attempt{At: base.Add(-time.Duration(i) * time.Minute), Monitor: "payouts", Channel: "ops-mail", Event: store.EventDown, Outcome: store.OutcomeSent, Reason: fmt.Sprintf("old %02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := m.Update(m.load())
	m, _ = press(t, sized(next.(Model), 100, 20), keyS, keyShiftTab)
	top := screenLines(m)
	header := rowOf(top, "OUTCOME")
	rows := deliveryRows(m)
	shown := len(rows)
	if shown < 5 || shown >= 40 {
		t.Fatalf("shown rows = %d of 40:\n%s", shown, view(m))
	}
	requireText(t, m, fmt.Sprintf("1–%d of 40", shown))
	check := func(name string) {
		t.Helper()
		lines := screenLines(m)
		if len(lines) != 20 || lines[0] != top[0] || lines[1] != top[1] || lines[2] != top[2] || lines[header] != top[header] {
			t.Fatalf("%s: the header, rule, tabs or table header moved:\n%s", name, view(m))
		}
		requireFooter(t, m, deliveriesFooter)
	}
	for range shown - 1 {
		m, _ = press(t, m, keyJ)
	}
	if rows := deliveryRows(m); !strings.Contains(rows[0], "backups") || !strings.Contains(rows[shown-1], "▌") {
		t.Fatalf("moving inside the page must not scroll:\n%s", view(m))
	}
	m, _ = press(t, m, keyDown)
	check("down")
	rows = deliveryRows(m)
	if len(rows) != shown || !strings.Contains(rows[shown-1], "▌") || !strings.Contains(rows[0], "payouts") {
		t.Fatalf("down past the page must scroll by one:\n%s", view(m))
	}
	requireText(t, m, fmt.Sprintf("2–%d of 40", shown+1))
	for range 60 {
		m, _ = press(t, m, keyJ)
	}
	check("bottom")
	requireText(t, m, fmt.Sprintf("%d–40 of 40", 41-shown), "old 33")
	for range 60 {
		m, _ = press(t, m, keyK)
	}
	m, _ = press(t, m, keyUp)
	check("top")
	if lines := screenLines(m); strings.Join(lines, "\n") != strings.Join(top, "\n") {
		t.Fatalf("back at the top must restore the first page:\n%s", view(m))
	}
}

func TestDeliveriesSearchFiltersInTheFooter(t *testing.T) {
	_, m := notifyStore(t)
	m, _ = press(t, sized(m, 100, 30), keyS, keyShiftTab)
	start := screenLines(m)
	header := rowOf(start, "OUTCOME")
	steady := func(name string) {
		t.Helper()
		lines := screenLines(m)
		if len(lines) != 30 || lines[0] != start[0] || lines[1] != start[1] || lines[header] != start[header] || rowOf(lines, tabsRow) != 2 {
			t.Fatalf("%s: the header, rule, tabs or table header moved:\n%s", name, view(m))
		}
	}
	m, _ = press(t, m, keySlash)
	steady("open")
	requireFooter(t, m, "/ █   6 of 6 · enter keep · esc clear")
	for query, want := range map[string]int{"pager": 2, "PAYOUTS": 2, "recovered": 1, "held": 1, "maintenance": 1, "502": 1, "cert": 1} {
		typed := typeText(t, m, query)
		steady(query)
		if rows := deliveryRows(typed); len(rows) != want {
			t.Errorf("%q: %d rows, want %d:\n%s", query, len(rows), want, view(typed))
		}
		requireFooter(t, typed, fmt.Sprintf("/ %s█   %d of 6 · enter keep · esc clear", query, want))
	}
	none := typeText(t, m, "zzz")
	requireText(t, none, "No deliveries match. esc clears.")
	if lines := screenLines(none); lines[header] != start[header] {
		t.Fatalf("no match must keep the table header:\n%s", view(none))
	}
	m = typeText(t, m, "pagerx")
	m, _ = press(t, m, keyBack)
	m, _ = press(t, m, keyEnter)
	steady("kept")
	requireFooter(t, m, deliveriesFooter)
	if line := screenLines(m)[2]; !strings.HasSuffix(strings.TrimRight(line, " "), "pager · 2 of 6 · esc clears") {
		t.Fatalf("kept search not shown on the tabs row: %q", line)
	}
	if rows := deliveryRows(m); len(rows) != 2 {
		t.Fatalf("kept search lost: %d rows", len(rows))
	}
	m, _ = press(t, m, keyEsc)
	steady("cleared")
	if view(m) != strings.Join(start, "\n") {
		t.Fatalf("esc must clear the search and stay on Deliveries:\n%s", view(m))
	}
	m, _ = press(t, m, keySlash)
	m = typeText(t, m, "pager")
	m, _ = press(t, m, keyEsc)
	if view(m) != strings.Join(start, "\n") {
		t.Fatalf("esc in the bar must clear it:\n%s", view(m))
	}
	next, _ := m.Update(tea.PasteMsg{Content: "x"})
	if view(next.(Model)) != view(m) {
		t.Fatal("paste with the bar closed must not search")
	}
	m, _ = press(t, m, keySlash)
	next, _ = m.Update(tea.PasteMsg{Content: "finance"})
	if rows := deliveryRows(next.(Model)); len(rows) != 2 {
		t.Fatalf("paste into the bar: %d rows", len(rows))
	}
}

func TestEnterOnAChannelOpensItsDeliveries(t *testing.T) {
	_, m := notifyStore(t)
	m, _ = press(t, sized(m, 100, 30), keyC)
	all := view(m)
	start := screenLines(m)
	m, _ = press(t, m, keyJ, keyJ, keyEnter)
	requireFooter(t, m, deliveriesFooter)
	rows := deliveryRows(m)
	if len(rows) != 2 || !strings.Contains(rows[0], "status 502") || !strings.Contains(rows[1], "monitor.recovered") {
		t.Fatalf("pager deliveries = %q:\n%s", rows, view(m))
	}
	lines := screenLines(m)
	if lines[0] != start[0] || lines[1] != start[1] || !strings.HasSuffix(strings.TrimRight(lines[2], " "), "channel pager · 2 of 6 · esc clears") {
		t.Fatalf("filter not shown like a kept search:\n%s", view(m))
	}
	m, _ = press(t, m, keySlash)
	m = typeText(t, m, "502")
	requireFooter(t, m, "/ 502█   1 of 6 · enter keep · esc clear")
	m, _ = press(t, m, keyEnter)
	if line := screenLines(m)[2]; !strings.HasSuffix(strings.TrimRight(line, " "), "channel pager · 502 · 1 of 6 · esc clears") {
		t.Fatalf("channel and search: %q", line)
	}
	m, _ = press(t, m, keyEsc)
	if rows := deliveryRows(m); len(rows) != 6 {
		t.Fatalf("esc must clear the channel filter: %d rows", len(rows))
	}
	requireFooter(t, m, deliveriesFooter)
	m, _ = press(t, m, keyShiftTab, keyK, keyK)
	if view(m) != all {
		t.Fatalf("Channels tab changed:\n%s", view(m))
	}
	m, _ = press(t, m, keyDown, keyDown, keyDown, keyEnter)
	requireText(t, m, "channel quiet · 0 of 6 · esc clears", "No deliveries match. esc clears.")
	m, _ = press(t, m, keyEsc, keyEsc)
	requireText(t, m, "MONITOR", "STATUS")
	requireNoText(t, m, tabsRow)
}

func TestDeliveriesEmptyStates(t *testing.T) {
	s, m := formStore(t)
	ctx := context.Background()
	if err := s.Beat(ctx, now); err != nil {
		t.Fatal(err)
	}
	reload := func() {
		next, _ := m.Update(m.load())
		m = next.(Model)
	}
	reload()
	m, _ = press(t, sized(m, 100, 30), keyS)
	requireText(t, m, "No mail servers configured.")
	m, _ = press(t, m, keyTab)
	requireText(t, m, "No channels configured.")
	m, _ = press(t, m, keyTab)
	requireText(t, m, "No deliveries yet. Add a channel on the Channels tab.")
	requireNoText(t, m, "OUTCOME")
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "mail-main", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, config.Channel{Name: "ops", Kind: config.KindEmail, SMTPServer: "mail-main", Recipients: []string{"ops@acme.io"}}); err != nil {
		t.Fatal(err)
	}
	reload()
	requireText(t, m, "No deliveries yet. Choose a channel when editing a monitor.")
	c := testChecks[0]
	c.Channel = "ops"
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	// A channel test is not a delivery.
	if _, err := s.EnqueueChannelTest(ctx, "ops", now); err != nil {
		t.Fatal(err)
	}
	reload()
	if len(m.snap.ChannelTests) != 1 {
		t.Fatalf("channel tests = %+v", m.snap.ChannelTests)
	}
	requireText(t, m, "No deliveries yet.")
	requireNoText(t, m, "Choose a channel", "Add a channel", "OUTCOME")
	requireFooter(t, m, deliveriesFooter)
	m, _ = press(t, m, keySlash)
	requireFooter(t, m, deliveriesFooter)
}

func TestNotificationsFitEightyByTwentyFour(t *testing.T) {
	s, m := notifyStore(t)
	ctx := context.Background()
	c := testChecks[0]
	c.Name, c.Channel = "payments-api-eu-west", "pager"
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, config.Channel{Name: "finance-escalations", Kind: config.KindEmail, SMTPServer: "mail-main", Recipients: []string{"long-recipient@finance.acme.io", "b@acme.io"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordAttempt(ctx, store.Attempt{At: time.Date(2026, 10, 25, 3, 40, 0, 0, time.UTC), Monitor: c.Name, Channel: "finance-escalations", Event: store.EventRecovered, Outcome: store.OutcomeFailed, Reason: "timeout after 10s"}); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.load())
	m = sized(next.(Model), 80, 24)
	fits := func(name string) {
		t.Helper()
		lines := screenLines(m)
		if len(lines) != 24 {
			t.Fatalf("%s: %d lines, want 24:\n%s", name, len(lines), view(m))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > 80 {
				t.Fatalf("%s: line wider than 80: %q", name, line)
			}
		}
		if rowOf(lines, tabsRow) != 2 {
			t.Fatalf("%s: tabs row moved:\n%s", name, view(m))
		}
	}
	m, _ = press(t, m, keyS)
	fits("mail servers")
	requireFooter(t, m, mailFooter)
	m, _ = press(t, m, keyTab)
	fits("channels")
	requireFooter(t, m, "↑/↓ · enter deliveries · n e d t · tab next · esc back · q quit")
	requireText(t, m, "MON", "LAST DELIVERY", "✗ failed Oct 25 03:04 · status 502", "◷ held Oct 25 02:58 · maintenance window")
	requireNoText(t, m, "SMTP SERVER / ORIGIN", "RECIPIENTS / AUTH")
	// The selected channel's server and recipients move under the table.
	requireText(t, m, "finance  email · mail-main → finance@acme.io")
	m, _ = press(t, m, keyJ)
	requireText(t, m, "finance-escalations  email · mail-main → long-recipient@finance.acme.io +1")
	m, _ = press(t, m, keyTab)
	fits("deliveries")
	requireFooter(t, m, deliveriesFooter)
	rows := deliveryRows(m)
	if len(rows) != 7 {
		t.Fatalf("rows = %d, want 7:\n%s", len(rows), view(m))
	}
	for _, want := range []string{"payments-…", "finance-e…", "recovered", "✗ failed", "timeout after 10s"} {
		if !strings.Contains(rows[0], want) {
			t.Errorf("compact row %q lacks %q", rows[0], want)
		}
	}
	if !strings.Contains(rows[1], "down") || strings.Contains(rows[1], "monitor.down") || !strings.Contains(rows[1], "status 502") {
		t.Errorf("compact events: %q", rows[1])
	}
	m, _ = press(t, m, keySlash)
	m = typeText(t, m, "finance-escalations-and-a-very-long-query-to-push-the-footer")
	fits("search")
	m, _ = press(t, m, keyEsc, keyEsc)
	requireText(t, m, "MONITOR")
}

func TestNotificationsKeepTheirFormsAsPopups(t *testing.T) {
	k, m := smtpSetup(t)
	s := m.store.(*store.Store)
	ctx := context.Background()
	if err := s.Beat(ctx, now); err != nil {
		t.Fatal(err)
	}
	m = sized(m, 100, 30)
	popup := func(title string) {
		t.Helper()
		out := view(m)
		if !strings.Contains(out, "╭─ "+title) || rowOf(screenLines(m), tabsRow) != 2 {
			t.Fatalf("%s must open over the view:\n%s", title, out)
		}
	}
	// Mail servers: add, edit, delete.
	m = smtpFill(t, m, "sëcret")
	popup("New SMTP server")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, tabsRow, "gmail", "smtp.example.com", "set")
	requireFooter(t, m, mailFooter)
	servers, err := s.SMTPServers(ctx)
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers %v %v", servers, err)
	}
	if got, err := k.Open(servers[0].Password); err != nil || got != "sëcret" {
		t.Fatalf("password %q %v", got, err)
	}
	m, _ = press(t, m, keyE)
	popup("Edit SMTP server")
	m = replaceField(t, m, "smtp.example.com", "mail.example.com")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	if servers, err = s.SMTPServers(ctx); err != nil || servers[0].Host != "mail.example.com" {
		t.Fatalf("edit %v %v", servers, err)
	}
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "relay", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.load())
	m = next.(Model)
	m, _ = press(t, m, keyDown, keyD)
	popup("Delete SMTP server")
	m, cmd = press(t, m, keyY)
	m = finishCommand(t, m, cmd)
	if servers, err = s.SMTPServers(ctx); err != nil || len(servers) != 1 || servers[0].Name != "gmail" {
		t.Fatalf("delete %v %v", servers, err)
	}
	// Channels: add, edit, test, delete.
	m, _ = press(t, m, keyTab, keyN)
	popup("New channel")
	m = typeText(t, m, "ops")
	m, _ = press(t, m, keyTab, keyTab, keyTab)
	m = typeText(t, m, "ops@example.com")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "ops", "ops@example.com")
	requireFooter(t, m, channelsFooter)
	m, _ = press(t, m, keyE)
	popup("Edit channel")
	m, _ = press(t, m, keyTab, keyTab)
	m = replaceField(t, m, "ops@example.com", "oncall@example.com")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	channels, err := s.Channels(ctx)
	if err != nil || len(channels) != 1 || strings.Join(channels[0].Recipients, ",") != "oncall@example.com" {
		t.Fatalf("edit channel %v %v", channels, err)
	}
	m, _ = press(t, m, keyT)
	popup("Test channel")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, testRunning)
	if tests, err := s.LatestChannelTests(ctx); err != nil || len(tests) != 1 || tests[0].Channel != "ops" {
		t.Fatalf("test %v %v", tests, err)
	}
	m, _ = press(t, m, keyEsc, keyD)
	popup("Delete channel")
	m, cmd = press(t, m, keyY)
	m = finishCommand(t, m, cmd)
	if channels, err = s.Channels(ctx); err != nil || len(channels) != 0 {
		t.Fatalf("delete channel %v %v", channels, err)
	}
	requireText(t, m, "No channels configured.", tabsRow)
	// Deliveries has no forms.
	m, _ = press(t, m, keyTab)
	before := view(m)
	for _, k := range []tea.KeyPressMsg{keyN, keyE, keyD, keyT, keyEnter} {
		if got, _ := press(t, m, k); view(got) != before {
			t.Fatalf("%q changed the Deliveries tab:\n%s", k.String(), view(got))
		}
	}
	_, cmd = press(t, m, keyQ)
	if cmd == nil {
		t.Fatal("q must quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q must quit")
	}
}

func TestHelpNamesTheNotificationsKeys(t *testing.T) {
	m, _ := press(t, modelWith(seededSnapshot(t, now)), keyHelp)
	for _, want := range []string{"s   mail servers", "c   channels"} {
		if !strings.Contains(view(m), want) {
			t.Errorf("help missing %q:\n%s", want, view(m))
		}
	}
	keys := ansi.Strip(renderKeys())
	for _, want := range []string{"s   mail servers", "c   channels", "NOTIFICATIONS (s, c)", "tab      next tab", "/   search deliveries", "enter    channel deliveries"} {
		if !strings.Contains(keys, want) {
			t.Errorf("key map missing %q:\n%s", want, keys)
		}
	}
}

func TestRemoteNotificationsRetryKeepsTheView(t *testing.T) {
	s := seeded()
	m := fail(remoteModel(t, s, 80, 24), s)
	m, _ = press(t, m, keyC)
	requireText(t, m, tabsRow, "disconnected", "r retry")
	s.err = nil
	calls := s.calls
	m, cmd := press(t, m, tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil {
		t.Fatal("r must reload")
	}
	next, _ := m.Update(cmd())
	m = next.(Model)
	if s.calls != calls+1 {
		t.Fatalf("calls = %d, want %d", s.calls, calls+1)
	}
	requireText(t, m, tabsRow, "● remote · https://up.acme.com")
	requireNoText(t, m, "disconnected")
	if local, _ := press(t, localModel(t, seeded(), 80, 24), keyC, tea.KeyPressMsg{Code: 'r', Text: "r"}); !local.notify {
		t.Fatal("r must not leave the view")
	}
}
