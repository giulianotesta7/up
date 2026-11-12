package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/store"
)

var (
	keyPgDown = tea.KeyPressMsg{Code: tea.KeyPgDown}
	keyPgUp   = tea.KeyPressMsg{Code: tea.KeyPgUp}
)

// incidentStore is a real store with monitors api and payments and a fresh
// heartbeat, holding results and attempts.
func incidentStore(t *testing.T, results []check.Result, attempts []store.Attempt) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetChecks(ctx, testChecks[:2]); err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if err := s.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range attempts {
		if err := s.RecordAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Beat(ctx, now); err != nil {
		t.Fatal(err)
	}
	return s
}

func day25(days int, clock string) time.Time { return at(clock).AddDate(0, 0, days-25) }

func result(day int, clock string, status check.Status, err string) check.Result {
	return check.Result{CheckName: "api", At: day25(day, clock), Status: status, Latency: 80 * time.Millisecond, Error: err}
}

func delivery(day int, clock, channel string, event store.Event, outcome store.Outcome, reason string) store.Attempt {
	return store.Attempt{At: day25(day, clock), Monitor: "api", Channel: channel, Event: event, Outcome: outcome, Reason: reason}
}

// alertingHistory gives api five incidents in the last 30 days, each with a
// different alert story, plus one on Sep 15, older than the period.
func alertingHistory() ([]check.Result, []store.Attempt) {
	results := []check.Result{
		result(-11, "00:00:00", check.StatusUp, ""),
		result(-10, "10:00:00", check.StatusDown, "old outage"), result(-10, "10:30:00", check.StatusUp, ""),
		result(22, "03:00:00", check.StatusUp, ""),
		result(22, "03:15:00", check.StatusDown, "status 502"), result(22, "03:20:00", check.StatusDown, "status 504"), result(22, "04:20:00", check.StatusUp, ""),
		result(23, "17:02:00", check.StatusDown, "connection refused"), result(23, "17:43:00", check.StatusUp, ""),
		result(24, "09:31:00", check.StatusDown, "status 503"), result(24, "09:33:00", check.StatusUp, ""),
		result(24, "22:10:00", check.StatusDown, "timeout after 5s"), result(24, "22:24:00", check.StatusUp, ""),
		result(25, "03:30:00", check.StatusUp, ""),
		result(25, "03:40:00", check.StatusDown, "status 500"),
	}
	payments := delivery(24, "09:31:30", "pager", store.EventDown, store.OutcomeFailed, "status 502")
	payments.Monitor = "payments"
	attempts := []store.Attempt{
		delivery(-10, "10:00:05", "ops", store.EventDown, store.OutcomeSent, ""),
		delivery(22, "03:15:05", "ops", store.EventDown, store.OutcomeHeld, "muted"),
		delivery(22, "04:20:05", "ops", store.EventRecovered, store.OutcomeHeld, "muted"),
		delivery(23, "17:02:05", "ops", store.EventDown, store.OutcomeSent, ""),
		delivery(23, "17:43:05", "ops", store.EventRecovered, store.OutcomeFailed, "timeout after 10s"),
		delivery(24, "09:32:00", "ops", store.EventCert, store.OutcomeFailed, "status 500"),
		payments,
		delivery(24, "22:10:05", "ops", store.EventDown, store.OutcomeSent, ""),
		delivery(24, "22:10:06", "pager", store.EventDown, store.OutcomeSent, ""),
		delivery(24, "22:24:05", "ops", store.EventRecovered, store.OutcomeSent, ""),
		delivery(24, "22:24:06", "pager", store.EventRecovered, store.OutcomeFailed, "status 502"),
		delivery(25, "03:40:05", "ops", store.EventDown, store.OutcomeSent, ""),
	}
	return results, attempts
}

func loadModel(t *testing.T, s *store.Store) Model {
	t.Helper()
	snap, err := Load(context.Background(), s, now)
	if err != nil {
		t.Fatal(err)
	}
	return modelWith(snap)
}

// section returns the lines from the one starting with title up to the next
// blank line or section header.
func section(out, title string) []string {
	var lines []string
	in := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, title) {
			in = true
			lines = append(lines, line)
			continue
		}
		if in && (line == "" || (line[0] != ' ' && !strings.HasPrefix(line, "▌"))) {
			break
		}
		if in {
			lines = append(lines, line)
		}
	}
	return lines
}

func TestDetailIncidentsShowStartDurationReasonAndAlert(t *testing.T) {
	results, attempts := alertingHistory()
	m, _ := press(t, loadModel(t, incidentStore(t, results, attempts)), keyEnter)
	out := view(m)
	want := []string{
		"INCIDENTS  30d · 5 incidents · 2h 7m down · 99.7% uptime",
		"  START         DURATION    FIRST FAILED CHECK  ALERTED",
		"▌ Oct 25 03:40  ongoing 5m  status 500          ✓ sent",
		"  Oct 24 22:10  14m         timeout after 5s    ✗ failed pager",
		"  Oct 24 09:31  2m          status 503          — none",
		"  Oct 23 17:02  41m         connection refused  ✗ failed",
		"  Oct 22 03:15  1h 5m       status 502          ◷ held muted",
	}
	if got := section(out, "INCIDENTS"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("incidents:\n%s\nwant:\n%s\n\nscreen:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"), out)
	}
	wantAlerts := []string{
		"RECENT ALERTS",
		"  Oct 25 03:40  monitor.down       ops    ✓ sent",
		"  Oct 24 22:24  monitor.recovered  pager  ✗ failed  status 502",
		"  Oct 24 22:24  monitor.recovered  ops    ✓ sent",
	}
	if got := section(out, "RECENT ALERTS"); strings.Join(got, "\n") != strings.Join(wantAlerts, "\n") {
		t.Fatalf("recent alerts:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantAlerts, "\n"))
	}
	for _, gone := range []string{"RECENT OUTAGES", "old outage", "Sep 15", "Oct 25 03:40:00 → ongoing"} {
		if strings.Contains(out, gone) {
			t.Errorf("detail still shows %q:\n%s", gone, out)
		}
	}
	// A monitor without results has no incidents; its alerts are its own.
	m, _ = press(t, m, keyEsc, keyDown, keyEnter)
	out = view(m)
	if got := strings.Join(section(out, "INCIDENTS"), "\n"); got != "INCIDENTS  30d · 0 incidents\n  No incidents." {
		t.Errorf("empty incidents = %q", got)
	}
	if got := strings.Join(section(out, "RECENT ALERTS"), "\n"); got != "RECENT ALERTS\n  Oct 24 09:31  monitor.down       pager  ✗ failed  status 502" {
		t.Errorf("payments alerts = %q", got)
	}
}

// When every channel agrees, Alerted shows the outcome alone; held shows
// why. An outage still going on counts its time up to now.
func TestIncidentAlertedWorstOutcome(t *testing.T) {
	results := []check.Result{
		result(15, "00:00:00", check.StatusUp, ""),
		result(25, "02:00:00", check.StatusUp, ""),
		result(25, "02:10:00", check.StatusDown, "status 502"), result(25, "02:20:00", check.StatusUp, ""),
		result(25, "02:30:00", check.StatusDown, "status 503"), result(25, "02:40:00", check.StatusUp, ""),
		result(25, "02:50:00", check.StatusDown, "status 504"), result(25, "03:00:00", check.StatusUp, ""),
		result(25, "03:10:00", check.StatusDown, "status 500"),
	}
	attempts := []store.Attempt{
		// Sent and held on different channels: held is worse than sent.
		delivery(25, "02:10:05", "ops", store.EventDown, store.OutcomeSent, ""),
		delivery(25, "02:10:06", "pager", store.EventDown, store.OutcomeHeld, "maintenance window"),
		// Both channels failed: no channel is named.
		delivery(25, "02:30:05", "ops", store.EventDown, store.OutcomeFailed, "status 502"),
		delivery(25, "02:30:06", "pager", store.EventDown, store.OutcomeFailed, "timeout after 10s"),
		// Held by a maintenance window on its only channel.
		delivery(25, "02:50:05", "ops", store.EventDown, store.OutcomeHeld, "maintenance window"),
		// The ongoing one: failed beats held on another channel.
		delivery(25, "03:10:05", "ops", store.EventDown, store.OutcomeHeld, "muted"),
		delivery(25, "03:10:06", "pager", store.EventDown, store.OutcomeFailed, "status 502"),
	}
	m, _ := press(t, loadModel(t, incidentStore(t, results, attempts)), keyEnter)
	want := []string{
		"INCIDENTS  30d · 4 incidents · 1h 5m down · 99.5% uptime",
		"  START         DURATION     FIRST FAILED CHECK  ALERTED",
		"▌ Oct 25 03:10  ongoing 35m  status 500          ✗ failed pager",
		"  Oct 25 02:50  10m          status 504          ◷ held maintenance window",
		"  Oct 25 02:30  10m          status 503          ✗ failed",
		"  Oct 25 02:10  10m          status 502          ◷ held pager",
	}
	if got := section(view(m), "INCIDENTS"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("incidents:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// nineIncidents gives api nine closed incidents, one a day.
func nineIncidents() []check.Result {
	results := []check.Result{result(15, "00:00:00", check.StatusUp, "")}
	for i := 0; i < 9; i++ {
		reason := "status 502"
		if i == 7 {
			reason = "dial tcp 203.0.113.7:443: connect: connection refused by the remote host"
		}
		results = append(results,
			result(16+i, "08:00:00", check.StatusDown, reason),
			result(16+i, "08:04:00", check.StatusUp, ""))
	}
	return results
}

// linesBefore returns the screen above the line starting with prefix.
func linesBefore(t *testing.T, out, prefix string) string {
	t.Helper()
	i := strings.Index(out, "\n"+prefix)
	if i < 0 {
		t.Fatalf("no %q line:\n%s", prefix, out)
	}
	return out[:i]
}

func markedRow(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "▌ Oct"); i >= 0 {
			return line[i:]
		}
	}
	return ""
}

func TestIncidentsScrollInAFixedArea(t *testing.T) {
	m, _ := press(t, sized(loadModel(t, incidentStore(t, nineIncidents(), nil)), 100, 34), keyEnter)
	first := view(m)
	header := "INCIDENTS  30d · 9 incidents · 36m down · 99.7% uptime   "
	requireText(t, m, header+"1–6 of 9 ↓", "RECENT ALERTS", "↑/↓ scroll incidents · esc back")
	if got := markedRow(first); !strings.HasPrefix(got, "▌ Oct 24 08:00") {
		t.Fatalf("marked row = %q", got)
	}
	steps := []struct {
		keys   []tea.KeyPressMsg
		hint   string
		marked string
	}{
		{[]tea.KeyPressMsg{keyDown, keyDown, keyDown, keyDown, keyDown, keyDown, keyDown, keyDown}, "4–9 of 9 ↑", "▌ Oct 16 08:00"},
		{[]tea.KeyPressMsg{keyDown}, "4–9 of 9 ↑", "▌ Oct 16 08:00"},
		{[]tea.KeyPressMsg{keyUp, keyUp, keyUp}, "4–9 of 9 ↑", "▌ Oct 19 08:00"},
		{[]tea.KeyPressMsg{keyK, keyK, keyK}, "3–8 of 9 ↑↓", "▌ Oct 22 08:00"},
		{[]tea.KeyPressMsg{keyJ}, "3–8 of 9 ↑↓", "▌ Oct 21 08:00"},
		{[]tea.KeyPressMsg{keyPgDown}, "4–9 of 9 ↑", "▌ Oct 16 08:00"},
		{[]tea.KeyPressMsg{keyPgUp}, "3–8 of 9 ↑↓", "▌ Oct 22 08:00"},
		{[]tea.KeyPressMsg{keyPgUp, keyUp, keyUp}, "1–6 of 9 ↓", "▌ Oct 24 08:00"},
	}
	above := linesBefore(t, first, "INCIDENTS")
	rows := strings.Count(first, "\n")
	for i, step := range steps {
		m, _ = press(t, m, step.keys...)
		out := view(m)
		requireText(t, m, header+step.hint)
		if got := markedRow(out); !strings.HasPrefix(got, step.marked) {
			t.Errorf("step %d: marked row = %q, want %q", i, got, step.marked)
		}
		if linesBefore(t, out, "INCIDENTS") != above || strings.Count(out, "\n") != rows {
			t.Errorf("step %d: the screen moved while scrolling:\n%s\n----\n%s", i, first, out)
		}
	}
	// Scrolling never changes the selected monitor, and reopening starts at
	// the top.
	m, _ = press(t, m, keyDown, keyDown, keyEsc)
	if m.selected != 0 {
		t.Fatalf("selection moved to %d", m.selected)
	}
	m, _ = press(t, m, keyEnter)
	requireText(t, m, header+"1–6 of 9 ↓")
	if lines := strings.Split(view(m), "\n"); len(lines) > 34 {
		t.Fatalf("%d rows at 100x34:\n%s", len(lines), view(m))
	}
}

func TestIncidentsFitEightyByTwentyFour(t *testing.T) {
	m, _ := press(t, sized(loadModel(t, incidentStore(t, nineIncidents(), nil)), 80, 24), keyEnter)
	check := func(label string) string {
		t.Helper()
		out := view(m)
		lines := strings.Split(out, "\n")
		if len(lines) > 24 || !strings.HasPrefix(lines[0], "▲ up") || !strings.HasPrefix(lines[len(lines)-1], "database:") {
			t.Fatalf("%s: %d rows:\n%s", label, len(lines), out)
		}
		for i, line := range lines {
			if w := ansi.StringWidth(line); w > 80 {
				t.Errorf("%s: row %d is %d cells: %q", label, i+1, w, line)
			}
		}
		return out
	}
	first := check("top")
	requireText(t, m, "INCIDENTS  30d · 9 incidents · 36m down · 99.7% uptime   1–6 of 9 ↓", "LATENCY", "RECENT ALERTS", "  No alerts yet.", "↑/↓ scroll incidents")
	if !strings.Contains(first, "  Oct 23 08:00  4m        dial tcp 203.0.113.7:443: con…  — none") {
		t.Errorf("long reason not cut to its column:\n%s", first)
	}
	m, _ = press(t, m, keyDown, keyDown, keyDown, keyDown, keyDown, keyDown, keyDown, keyDown)
	out := check("bottom")
	requireText(t, m, "4–9 of 9 ↑")
	if linesBefore(t, out, "INCIDENTS") != linesBefore(t, first, "INCIDENTS") || strings.Count(out, "\n") != strings.Count(first, "\n") {
		t.Errorf("the screen moved while scrolling:\n%s\n----\n%s", first, out)
	}
}

// In the split layout ↑/↓ keep selecting monitors; pgup/pgdn scroll the
// selected monitor's incidents.
func TestSplitScrollsIncidentsWithPageKeys(t *testing.T) {
	m := sized(loadModel(t, incidentStore(t, nineIncidents(), nil)), 140, 40)
	if !m.splitMode() {
		t.Fatal("140x40 should split")
	}
	requireText(t, m, "INCIDENTS  30d · 9 incidents · 36m down · 99.7% uptime   1–6 of 9 ↓", "pgup/dn incidents")
	first := view(m)
	if !strings.HasPrefix(markedRow(first), "▌ Oct 24 08:00") {
		t.Fatalf("marked row = %q", markedRow(first))
	}
	// A page moves the marker six rows down.
	m, _ = press(t, m, keyPgDown)
	out := view(m)
	if !strings.Contains(out, "2–7 of 9 ↑↓") || !strings.HasPrefix(markedRow(out), "▌ Oct 18 08:00") {
		t.Fatalf("pgdn did not scroll:\n%s", out)
	}
	if strings.Count(out, "\n") != strings.Count(first, "\n") || m.selected != 0 {
		t.Fatalf("pgdn moved the screen or the selection (%d)", m.selected)
	}
	m, _ = press(t, m, keyDown)
	if m.selected != 1 || !strings.Contains(view(m), "No incidents.") {
		t.Fatalf("↓ must select the next monitor:\n%s", view(m))
	}
	m, _ = press(t, m, keyUp)
	requireText(t, m, "1–6 of 9 ↓")
}

func TestHelpListsIncidentScrollKeys(t *testing.T) {
	if keys := ansi.Strip(renderKeys()); !strings.Contains(keys, "pgup/dn") || !strings.Contains(keys, "incidents") {
		t.Errorf("key map:\n%s", keys)
	}
	if popup := ansi.Strip(renderHelpPopup(true)); !strings.Contains(popup, "pgup/dn  incidents") {
		t.Errorf("split popup:\n%s", popup)
	}
}

// The period numbers are those of store.UptimeWindow, also with more
// incidents than the list keeps and when the daemon stopped.
func TestLoadPeriodNumbersMatchUptimeWindow(t *testing.T) {
	var many []check.Result
	for i := range IncidentLimit + 10 {
		down := result(1, "00:00:00", check.StatusDown, "status 500")
		down.At = down.At.Add(time.Duration(i) * 5 * time.Hour)
		up := result(1, "00:00:00", check.StatusUp, "")
		up.At = down.At.Add(time.Hour)
		many = append(many, down, up)
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		extra []check.Result
		clock time.Time
		want  int
	}{
		{"few incidents", nil, now, 5},
		{"more than the list keeps", many, now, 5 + IncidentLimit + 10},
		{"stale", nil, now.Add(10 * StaleAfter), 5},
		// The period ends at the last heartbeat, before it starts: nothing,
		// not even the ongoing incident.
		{"stopped before the period", nil, now.Add(IncidentPeriod + day), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results, attempts := alertingHistory()
			s := incidentStore(t, append(results, tc.extra...), attempts)
			snap, err := Load(ctx, s, tc.clock)
			if err != nil {
				t.Fatal(err)
			}
			until := tc.clock
			if snap.Stale(tc.clock) {
				until = snap.LastBeat
			}
			up, observed, incidents, err := s.UptimeWindow(ctx, "api", tc.clock.Add(-IncidentPeriod), until)
			if err != nil {
				t.Fatal(err)
			}
			api := snap.Services[0]
			if api.PeriodUp != up || api.PeriodObserved != observed || api.PeriodIncidents != incidents || incidents != tc.want {
				t.Fatalf("period = %v, %v, %d; want %v, %v, %d (%d)", api.PeriodUp, api.PeriodObserved, api.PeriodIncidents, up, observed, incidents, tc.want)
			}
			if tc.want > 0 && len(api.Incidents) != min(tc.want, IncidentLimit) {
				t.Fatalf("listed %d incidents, want %d", len(api.Incidents), min(tc.want, IncidentLimit))
			}
		})
	}
}
