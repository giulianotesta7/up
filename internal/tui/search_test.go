package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

const (
	wantSearchFooter = "type to search · tab status · enter keep · esc clear"
	wantNoMatch      = "No monitors match. esc clears."
)

var (
	keySlash = tea.KeyPressMsg{Code: '/', Text: "/"}
)

func svc(c config.Check, status check.Status) Service {
	s := Service{Check: c, HasResult: true, Latest: check.Result{CheckName: c.Name, At: now, Status: status, Latency: 80 * time.Millisecond}}
	if status == check.StatusDown {
		s.Latest.Error = "status 503"
	}
	return s
}

// searchSnapshot has one monitor per tab: DOWN (payments), FAILING 2/3
// (checkout), muted (billing), in maintenance (status-page), plus api (UP,
// pay.example.com is not its host) and a TCP monitor.
func searchSnapshot() Snapshot {
	http := func(name, url string) config.Check {
		return config.Check{Name: name, URL: url, Interval: time.Minute, Timeout: 5 * time.Second, ExpectedStatus: 200}
	}
	checkout := svc(http("checkout", "https://shop.example.com"), check.StatusDown)
	checkout.Check.AlertAfter = 3
	checkout.Streak = check.Streak{Failures: 2}
	billing := svc(http("billing", "https://bill.example.com"), check.StatusUp)
	billing.HasMute, billing.Mute = true, config.Mute{Until: now.Add(time.Hour)}
	page := svc(http("status-page", "https://status.example.com"), check.StatusUp)
	page.InMaintenance = config.Maintenance{Active: true, End: now.Add(time.Hour)}
	tcp := svc(config.Check{Name: "postgres", Kind: config.CheckTCP, Host: "db.internal", Port: 5432, Interval: time.Minute, Timeout: 5 * time.Second}, check.StatusUp)
	return Snapshot{Services: []Service{
		svc(http("api", "https://api.example.com/health"), check.StatusUp),
		svc(http("payments", "https://pay.example.com"), check.StatusDown),
		checkout, billing, page, tcp,
	}, HasBeat: true, LastBeat: now, HasUptime: true, Uptime: 99.2}
}

func searchModel(width, height int) Model {
	return sized(modelWith(searchSnapshot()), width, height)
}

func typeKeys(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		m, _ = press(t, m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

// listed returns the monitor names on screen in order, and the selected one.
func listed(m Model) (names []string, selected string) {
	for _, line := range strings.Split(view(m), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "●" || fields[1] != "●" && fields[0] != "▌" {
			if len(fields) >= 2 && fields[0] == "●" {
				names = append(names, fields[1])
			}
			continue
		}
		if fields[0] == "▌" && fields[1] == "●" {
			selected = fields[2]
			names = append(names, fields[2])
		}
	}
	return names, selected
}

func rows(m Model) []string {
	names, _ := listed(m)
	return names
}

func selectedRow(m Model) string {
	_, selected := listed(m)
	return selected
}

func TestSlashOpensBarAndFiltersLive(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keySlash)
	requireText(t, m, "/ █", "all", "down", "failing", "held", "6 of 6 monitors", wantSearchFooter)
	if strings.Contains(view(m), listFooter) {
		t.Errorf("list footer shown while the bar is open:\n%s", view(m))
	}
	m = typeKeys(t, m, "PAY")
	requireText(t, m, "/ PAY█", "1 of 6 monitors")
	if got := rows(m); strings.Join(got, ",") != "payments" {
		t.Errorf("rows = %v, want only payments (case-insensitive)", got)
	}
	m, _ = press(t, m, keyBack)
	requireText(t, m, "/ PA█")
	m, _ = press(t, m, keyBack, keyBack, keyBack)
	if got := rows(m); len(got) != 6 {
		t.Errorf("rows = %v, want all after clearing the query", got)
	}
}

func TestSearchMatchesNameURLAndHost(t *testing.T) {
	for query, want := range map[string]string{
		"CHECK":        "checkout",
		"shop.example": "checkout",
		"DB.INTERNAL":  "postgres",
		"/health":      "api",
	} {
		m, _ := press(t, searchModel(100, 30), keySlash)
		m = typeKeys(t, m, query)
		if got := rows(m); strings.Join(got, ",") != want {
			t.Errorf("query %q rows = %v, want %s", query, got, want)
		}
	}
}

func TestSearchHighlightsMatchInName(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keySlash)
	m = typeKeys(t, m, "pay")
	if raw := m.View().Content; !strings.Contains(raw, accentStyle.Bold(true).Render("pay")) {
		t.Errorf("matched text not highlighted:\n%q", raw)
	}
}

func TestTabCyclesStatusFilter(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keySlash)
	for _, step := range []struct {
		key  tea.KeyPressMsg
		want string
		rows string
	}{
		{keyTab, "1 of 6 monitors", "payments"},
		{keyTab, "1 of 6 monitors", "checkout"},
		{keyTab, "2 of 6 monitors", "billing,status-page"},
		{keyTab, "6 of 6 monitors", "api,payments,checkout,billing,status-page,postgres"},
		{keyShiftTab, "2 of 6 monitors", "billing,status-page"},
		{keyShiftTab, "1 of 6 monitors", "checkout"},
	} {
		m, _ = press(t, m, step.key)
		requireText(t, m, step.want)
		if got := strings.Join(rows(m), ","); got != step.rows {
			t.Errorf("rows = %s, want %s", got, step.rows)
		}
	}
}

func TestTabCombinesWithQuery(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keySlash)
	m = typeKeys(t, m, "pay")
	m, _ = press(t, m, keyTab, keyTab)
	requireText(t, m, "0 of 6 monitors", wantNoMatch)
	m, _ = press(t, m, keyShiftTab)
	requireText(t, m, "1 of 6 monitors")
}

func TestEnterKeepsFilterAndShowsHeader(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keySlash)
	m = typeKeys(t, m, "pay")
	m, _ = press(t, m, keyEnter)
	lines := strings.Split(view(m), "\n")
	if !strings.Contains(lines[0], "99.2% uptime today  pay · 1 of 6 monitors · esc clears") {
		t.Errorf("header = %q, want the kept filter after the uptime", lines[0])
	}
	out := view(m)
	if strings.Contains(out, "/ pay█") || strings.Contains(out, wantSearchFooter) || !strings.Contains(out, listFooter) {
		t.Errorf("bar not closed or footer not restored:\n%s", out)
	}
	if got := rows(m); strings.Join(got, ",") != "payments" {
		t.Errorf("rows = %v", got)
	}
	// Reopening shows the current query and tab.
	m, _ = press(t, m, keySlash)
	requireText(t, m, "/ pay█", "1 of 6 monitors", wantSearchFooter)
}

func TestKeptTabShowsInHeader(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keySlash, keyTab, keyEnter)
	if first := strings.Split(view(m), "\n")[0]; !strings.Contains(first, "down · 1 of 6 monitors · esc clears") {
		t.Errorf("header = %q", first)
	}
}

func TestEscClearsFilterWithoutChangingData(t *testing.T) {
	for _, kept := range []bool{false, true} {
		m, _ := press(t, searchModel(100, 30), keySlash)
		m = typeKeys(t, m, "pay")
		if kept {
			m, _ = press(t, m, keyEnter)
		}
		m, _ = press(t, m, keyEsc)
		if got := rows(m); len(got) != 6 {
			t.Errorf("kept=%v rows = %v, want the full list", kept, got)
		}
		if out := view(m); strings.Contains(out, "esc clears") || strings.Contains(out, "of 6 monitors") || !strings.Contains(out, listFooter) {
			t.Errorf("kept=%v filter chrome left:\n%s", kept, out)
		}
		if selectedRow(m) != "payments" || len(m.snap.Services) != 6 {
			t.Errorf("kept=%v selected %q with %d services, want payments kept", kept, selectedRow(m), len(m.snap.Services))
		}
	}
}

func TestEscInDetailGoesBackBeforeClearing(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keySlash)
	m = typeKeys(t, m, "pay")
	m, _ = press(t, m, keyEnter, keyEnter)
	if !m.detail {
		t.Fatal("enter did not open the detail")
	}
	m, _ = press(t, m, keyEsc)
	requireText(t, m, "esc clears")
	m, _ = press(t, m, keyEsc)
	if len(rows(m)) != 6 {
		t.Errorf("second esc did not clear:\n%s", view(m))
	}
}

func TestNoMatchEmptyState(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keySlash)
	m = typeKeys(t, m, "pagos")
	requireText(t, m, "/ pagos█", "0 of 6 monitors", wantNoMatch)
	if out := view(m); strings.Contains(out, "No checks configured.") || strings.Contains(out, "MONITOR") {
		t.Errorf("unexpected list chrome:\n%s", out)
	}
	// Actions do nothing without a visible monitor.
	m, _ = press(t, m, keyEnter)
	for _, key := range []tea.KeyPressMsg{keyE, keyD, keyM, keyW, keyEnter} {
		m, _ = press(t, m, key)
		if m.form != nil || m.deletion != nil || m.mute != nil || m.maint != nil || m.detail {
			t.Fatalf("key %v acted with nothing visible", key)
		}
	}
	m, _ = press(t, m, keyEsc)
	if len(rows(m)) != 6 {
		t.Error("esc did not restore the list")
	}
}

func TestJKAndArrowsStepOnlyBetweenVisibleMonitors(t *testing.T) {
	// held shows billing and status-page: api, payments, checkout and postgres
	// are skipped, whatever their position.
	m, _ := press(t, searchModel(100, 30), keySlash, keyTab, keyTab, keyTab, keyEnter)
	if got := selectedRow(m); got != "billing" {
		t.Fatalf("selected %q, want billing (first visible)", got)
	}
	for _, step := range []struct {
		key  tea.KeyPressMsg
		want string
	}{{keyJ, "status-page"}, {keyJ, "status-page"}, {keyK, "billing"}, {keyK, "billing"}, {keyDown, "status-page"}, {keyUp, "billing"}} {
		m, _ = press(t, m, step.key)
		if got := selectedRow(m); got != step.want {
			t.Fatalf("after %v selected %q, want %q", step.key, got, step.want)
		}
	}
	// Arrows also move while the bar is open.
	m, _ = press(t, m, keySlash, keyDown)
	if got := selectedRow(m); got != "status-page" {
		t.Errorf("down in the bar selected %q", got)
	}
}

func TestSelectionMovesToVisibleMonitorWhenFilterChanges(t *testing.T) {
	m := searchModel(100, 30)
	m, _ = press(t, m, keyJ, keyJ, keyJ) // billing
	m, _ = press(t, m, keySlash, keyTab) // down: only payments
	if got := selectedRow(m); got != "payments" {
		t.Errorf("selected %q, want payments", got)
	}
}

func TestActionsActOnSelectedMonitorOfFilteredList(t *testing.T) {
	filtered := func(t *testing.T) Model {
		m, _ := press(t, searchModel(100, 30), keySlash)
		m = typeKeys(t, m, "example")
		m, _ = press(t, m, keyTab, keyTab, keyTab, keyEnter) // held: billing, status-page
		m, _ = press(t, m, keyJ)                             // status-page
		if got := selectedRow(m); got != "status-page" {
			t.Fatalf("selected %q, want status-page", got)
		}
		return m
	}
	t.Run("enter", func(t *testing.T) {
		m, _ := press(t, filtered(t), keyEnter)
		requireText(t, m, "status-page  https://status.example.com", "esc back")
	})
	t.Run("e", func(t *testing.T) {
		m, _ := press(t, filtered(t), keyE)
		if m.form == nil || m.form.base.Name != "status-page" {
			t.Fatalf("form = %+v, want editing status-page", m.form)
		}
	})
	t.Run("d", func(t *testing.T) {
		m, _ := press(t, filtered(t), keyD)
		if m.deletion == nil || m.deletion.name != "status-page" {
			t.Fatalf("deletion = %+v", m.deletion)
		}
		m, _ = press(t, m, keyEsc)
		if m.deletion != nil || len(m.snap.Services) != 6 {
			t.Fatal("cancelling changed data")
		}
	})
	t.Run("m", func(t *testing.T) {
		m, _ := press(t, filtered(t), keyM)
		if m.mute == nil || m.mute.name != "status-page" {
			t.Fatalf("mute = %+v", m.mute)
		}
	})
	t.Run("w", func(t *testing.T) {
		m, _ := press(t, filtered(t), keyW)
		if m.maint == nil || m.maint.name != "status-page" {
			t.Fatalf("maint = %+v", m.maint)
		}
	})
}

func TestUnmuteActsOnSelectedMonitorOfFilteredList(t *testing.T) {
	s, m := formStore(t)
	ctx := context.Background()
	for _, c := range testChecks[:2] {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := s.SetMute(ctx, c.Name, now.Add(time.Hour), false); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	m, _ = press(t, m, keySlash)
	m = typeKeys(t, m, "pay")
	m, _ = press(t, m, keyEnter, keyU)
	_, cmd := press(t, m, keyU)
	finishCommand(t, m, cmd)
	if mute, _, _ := s.Mute(ctx, "payments"); !mute.Until.Equal(now) {
		t.Errorf("payments mute until %v, want unmuted", mute.Until)
	}
	if mute, ok, _ := s.Mute(ctx, "api"); !ok || !mute.Until.After(now) {
		t.Errorf("api mute = %+v %v, want untouched", mute, ok)
	}
}

func TestFilterSurvivesSnapshotRefresh(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keySlash)
	m = typeKeys(t, m, "pay")
	m, _ = press(t, m, keyEnter)
	snap := searchSnapshot()
	snap.Services = append(snap.Services, svc(config.Check{Name: "payouts", URL: "https://x.example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}, check.StatusUp),
		svc(config.Check{Name: "mail", URL: "https://mail.example.com"}, check.StatusUp))
	next, _ := m.Update(snapshotMsg{snap: snap})
	m = next.(Model)
	if got := strings.Join(rows(m), ","); got != "payments,payouts" {
		t.Errorf("rows = %s, want payments and payouts", got)
	}
	requireText(t, m, "pay · 2 of 8 monitors · esc clears")
	snap.Services = append(snap.Services, svc(config.Check{Name: "paypal", URL: "https://p.example.com"}, check.StatusUp))
	next, _ = m.Update(snapshotMsg{snap: snap})
	m = next.(Model)
	requireText(t, m, "3 of 9 monitors")
	if selectedRow(m) != "payments" {
		t.Errorf("selected %q, want payments kept", selectedRow(m))
	}
}

func TestSelectionMovesToVisibleMonitorWhenSelectedDisappears(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keySlash)
	m = typeKeys(t, m, "example") // everything matches
	m, _ = press(t, m, keyTab, keyEnter)
	if selectedRow(m) != "payments" {
		t.Fatalf("selected %q", selectedRow(m))
	}
	// payments recovers and checkout goes down: the selection must land on a
	// visible monitor, not stay on the now hidden one.
	snap := searchSnapshot()
	snap.Services[1] = svc(snap.Services[1].Check, check.StatusUp)
	snap.Services[2] = svc(snap.Services[2].Check, check.StatusDown)
	snap.Services[2].Check.AlertAfter = 0
	next, _ := m.Update(snapshotMsg{snap: snap})
	m = next.(Model)
	if got := selectedRow(m); got != "checkout" {
		t.Errorf("selected %q, want checkout", got)
	}
	m, _ = press(t, m, keyE)
	if m.form == nil || m.form.base.Name != "checkout" {
		t.Errorf("e opened %+v, want checkout", m.form)
	}
	// Nothing visible any more: no action, no panic.
	m.form = nil
	snap.Services = snap.Services[:2]
	next, _ = m.Update(snapshotMsg{snap: snap})
	m = next.(Model)
	requireText(t, m, wantNoMatch)
	if m, _ = press(t, m, keyE); m.form != nil {
		t.Error("e opened a form with nothing visible")
	}
}

func TestSearchInRemoteSplitAndNarrowLayouts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		w, h         int
		destination  string
		wantNoEnter  bool
		footerSuffix string
	}{
		{"local 80x24", 80, 24, "", false, "database: "},
		{"remote 80x24", 80, 24, "https://up.acme.com", false, "remote · https://up.acme.com"},
		{"local split", 140, 40, "", true, "database: "},
		{"remote split", 140, 40, "https://up.acme.com", true, "remote · https://up.acme.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(nil, func() time.Time { return now }, Options{Destination: tc.destination})
			next, _ := m.Update(snapshotMsg{snap: searchSnapshot()})
			m = sized(next.(Model), tc.w, tc.h)
			if m.splitMode() != tc.wantNoEnter {
				t.Fatalf("splitMode = %v", m.splitMode())
			}
			m, _ = press(t, m, keySlash)
			m = typeKeys(t, m, "pay")
			requireText(t, m, "/ pay█", "1 of 6 monitors", wantSearchFooter, "payments")
			out := view(m)
			if strings.Contains(out, "billing") || strings.Contains(out, "checkout") {
				t.Errorf("hidden monitors shown:\n%s", out)
			}
			if tc.wantNoEnter {
				requireText(t, m, "Status       DOWN")
			}
			for _, line := range strings.Split(m.View().Content, "\n") {
				// A long error may run past 80 columns in the table, as before.
				if ansi.StringWidth(line) > tc.w && (tc.wantNoEnter || !strings.Contains(line, "status 503")) {
					t.Errorf("overwide line %q", line)
				}
			}
			if lines := strings.Count(out, "\n") + 1; lines > tc.h {
				t.Errorf("%d lines, want at most %d:\n%s", lines, tc.h, out)
			}
			m, _ = press(t, m, keyEnter)
			if first := strings.Split(view(m), "\n")[0]; !strings.Contains(first, "esc clears") && tc.w >= 100 {
				t.Errorf("header = %q", first)
			}
			m, _ = press(t, m, keyE)
			if m.form == nil || m.form.base.Name != "payments" {
				t.Errorf("e opened %+v, want payments", m.form)
			}
			if !strings.Contains(view(m), tc.footerSuffix) {
				t.Errorf("footer lost:\n%s", view(m))
			}
		})
	}
}

func TestSearchWithRealStoreSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db")
	w, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.SetChecks(context.Background(), testChecks); err != nil {
		t.Fatal(err)
	}
	m := New(w, func() time.Time { return now })
	next, _ := m.Update(m.Init()())
	m, _ = press(t, next.(Model), keySlash)
	m = typeKeys(t, m, "new.example")
	m, _ = press(t, m, keyEnter, keyE)
	if m.form == nil || m.form.base.Name != "new" {
		t.Errorf("e opened %+v, want new", m.form)
	}
}

func TestHelpListsSearchNotFooter(t *testing.T) {
	m := searchModel(100, 30)
	if strings.Contains(view(m), "search") || ansi.StringWidth(listFooter) > 80 {
		t.Errorf("list footer mentions search:\n%s", view(m))
	}
	for _, size := range [][2]int{{100, 30}, {80, 24}, {140, 40}} {
		help, _ := press(t, sized(modelWith(searchSnapshot()), size[0], size[1]), keyHelp)
		requireText(t, help, "/  ")
		if out := view(help); !strings.Contains(out, "search") {
			t.Errorf("%v: help lacks / search:\n%s", size, out)
		}
	}
	short, _ := press(t, sized(modelWith(searchSnapshot()), 40, 14), keyHelp)
	requireText(t, short, "/        search")
}

func TestListKeysStillActOnSelectionWithoutFilter(t *testing.T) {
	m, _ := press(t, searchModel(100, 30), keyJ, keyJ)
	if got := selectedRow(m); got != "checkout" {
		t.Fatalf("selected %q, want checkout", got)
	}
	if e, _ := press(t, m, keyE); e.form == nil || e.form.base.Name != "checkout" {
		t.Errorf("e opened %+v", e.form)
	}
	if d, _ := press(t, m, keyD); d.deletion == nil || d.deletion.name != "checkout" {
		t.Errorf("d opened %+v", d.deletion)
	}
	if mu, _ := press(t, m, keyM); mu.mute == nil || mu.mute.name != "checkout" {
		t.Errorf("m opened %+v", mu.mute)
	}
	if w, _ := press(t, m, keyW); w.maint == nil || w.maint.name != "checkout" {
		t.Errorf("w opened %+v", w.maint)
	}
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "checkout  https://shop.example.com")
	m, _ = press(t, m, keyEsc, keyK, keyK, keyK)
	if got := selectedRow(m); got != "api" {
		t.Errorf("selected %q, want api", got)
	}
	if strings.Contains(view(m), "of 6 monitors") {
		t.Error("counter shown without a filter")
	}
}
