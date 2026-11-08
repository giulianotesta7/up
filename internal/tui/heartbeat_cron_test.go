package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

// cronZone is the Defaults zone of the cron tests: -03 all year, so the test
// clock (Sun Oct 25 03:45 UTC) is Sun 00:45 there.
const cronZone = "America/Argentina/Buenos_Aires"

// cronModel is hbModel with the Defaults zone set to cronZone.
func cronModel(t *testing.T, width, height int) (*store.Store, string, Model) {
	t.Helper()
	s, path, m := hbModel(t, true, width, height)
	setZone(t, s, cronZone)
	return s, path, reload(t, m)
}

// lineWithText is the screen line that contains text.
func lineWithText(t *testing.T, m Model, text string) int {
	t.Helper()
	for i, line := range strings.Split(view(m), "\n") {
		if strings.Contains(line, text) {
			return i
		}
	}
	t.Fatalf("no line with %q:\n%s", text, view(m))
	return -1
}

func setZone(t *testing.T, s *store.Store, zone string) {
	t.Helper()
	d := config.BuiltinDefaults()
	d.Zone = zone
	if err := s.SetDefaults(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

// openCronForm opens a new heartbeat form with Schedule ‹ Cron ›, the focus
// on Schedule.
func openCronForm(t *testing.T, m Model) Model {
	t.Helper()
	m = openHeartbeatForm(t, m)
	m, _ = press(t, m, keyTab, keyTab)
	requireText(t, m, "Schedule: ‹ Period ›")
	m, _ = press(t, m, keyRight)
	return m
}

func TestCronHeartbeatScheduleSwapsOnlyThePeriodRow(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		_, _, m := cronModel(t, size[0], size[1])
		m = openHeartbeatForm(t, m)
		requireText(t, m, "Schedule: ‹ Period ›", "Period: 24h", "Grace: 1h", "Alerts when no ping arrives within period + grace.",
			"First alert ≈ 25h after the last ping")
		requireAbsent(t, m, "Cron:", "Zone ", "Next runs", "min hour day month weekday")
		box := boxGeometry(t, m, "New monitor")
		grace := lineWithText(t, m, "Grace: 1h")

		m, _ = press(t, m, keyTab, keyTab, keyRight)
		requireText(t, m, "Schedule: ‹ Cron ›", "Cron: ", "Grace: 1h", "Alerts when no ping arrives within grace after a run.",
			"Zone       "+cronZone+" (Defaults)", "←/→ choose")
		requireAbsent(t, m, "Period:", "First alert", "Next runs", "min hour day month weekday")
		if got := boxGeometry(t, m, "New monitor"); got != box {
			t.Fatalf("%v: Cron moved the box: %v, was %v:\n%s", size, got, box, view(m))
		}
		if got := lineWithText(t, m, "Grace: 1h"); got != grace {
			t.Fatalf("%v: Grace moved from line %d to %d:\n%s", size, grace, got, view(m))
		}

		// The field order shows while Cron has focus; next runs follow typing.
		m, _ = press(t, m, keyTab)
		requireText(t, m, "Cron: │   min hour day month weekday")
		m = typeText(t, m, "0 2 * * 1-5")
		requireText(t, m, "Cron: 0 2 * * 1-5│   min hour day month weekday", "Next runs  Mon 26 02:00 · Tue 27 02:00 · Wed 28 02:00",
			"tab/shift+tab focus · enter save")
		if got := boxGeometry(t, m, "New monitor"); got != box {
			t.Fatalf("%v: next runs moved the box: %v, was %v:\n%s", size, got, box, view(m))
		}
		m, _ = press(t, m, keyTab)
		requireText(t, m, "Cron: 0 2 * * 1-5", "Next runs  Mon 26 02:00")
		requireAbsent(t, m, "min hour day month weekday")

		// Back to Period: its row returns as it was, and nothing moves.
		m, _ = press(t, m, keyShiftTab, keyShiftTab, keyLeft)
		requireText(t, m, "Schedule: ‹ Period ›", "Period: 24h", "First alert ≈ 25h")
		requireAbsent(t, m, "Cron:", "Zone ", "Next runs")
		if got := boxGeometry(t, m, "New monitor"); got != box {
			t.Fatalf("%v: Period moved the box: %v, was %v:\n%s", size, got, box, view(m))
		}
		requireWidth(t, m, size[0])
	}
}

func TestCronHeartbeatNextRunsAddTheMonthAndHideWhileInvalid(t *testing.T) {
	_, _, m := cronModel(t, 120, 40)
	m = openCronForm(t, m)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "0 2 1 * *")
	requireText(t, m, "Next runs  Sun Nov 1 02:00 · Tue Dec 1 02:00 · Fri Jan 1 02:00")
	m = replaceField(t, m, "1 * *", "2")
	requireText(t, m, "Cron: 0 2 2│", "Zone       "+cronZone+" (Defaults)")
	requireAbsent(t, m, "Next runs", "Cannot save")
	m = typeText(t, m, "5 * *")
	requireText(t, m, "Next runs  Sun 25 02:00 · Wed Nov 25 02:00")

	// Without a Defaults zone the runs are in UTC.
	_, _, m = hbModel(t, true, 120, 40)
	m = openCronForm(t, m)
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "@daily")
	requireText(t, m, "Zone       UTC (Defaults)", "Next runs  Mon 26 00:00 · Tue 27 00:00 · Wed 28 00:00")
}

func TestCronHeartbeatInvalidExpressionIsRejectedOnTheCronRow(t *testing.T) {
	s, _, m := cronModel(t, 120, 40)
	m = openCronForm(t, m)
	m, _ = press(t, m, keyShiftTab)
	m = typeText(t, m, "nightly")
	m, _ = press(t, m, keyTab, keyTab)
	m = typeText(t, m, "0 25 * * 1-5")
	requireAbsent(t, m, "Next runs")
	m, _ = press(t, m, keyTab, keyTab) // leave Cron for Channel
	m, cmd := press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("an invalid cron saved")
	}
	requireText(t, m, `Cannot save monitor: cron "0 25 * * 1-5" is not valid: hour 25 is out of range 0-23`,
		"Cron: 0 25 * * 1-5│   min hour day month weekday")
	if m.form.focus != focusCron {
		t.Fatalf("focus = %d, want the Cron row", m.form.focus)
	}
	if got, _ := s.Checks(context.Background()); len(got) != 1 {
		t.Fatalf("checks = %+v", got)
	}

	// An empty expression says it is required.
	m = replaceField(t, m, "0 25 * * 1-5", "")
	m, _ = press(t, m, keyTab)
	m, cmd = press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("an empty cron saved")
	}
	requireText(t, m, "Cannot save monitor: cron is required")
	if m.form.focus != focusCron {
		t.Fatalf("focus = %d, want the Cron row", m.form.focus)
	}
}

func TestCronHeartbeatSaveStoresTheScheduleAndShowsTheURL(t *testing.T) {
	s, path, m := cronModel(t, 120, 40)
	m = openCronForm(t, m)
	m, _ = press(t, m, keyShiftTab)
	m = typeText(t, m, "nightly")
	m, _ = press(t, m, keyTab, keyTab)
	m = typeText(t, m, "0 2 * * 1-5")
	m, cmd := press(t, m, keyEnter)
	m = run(t, m, cmd)
	got, ok := storedByName(t, s, "nightly")
	if !ok || got.Kind != config.CheckHeartbeat || got.Schedule != config.ScheduleCron || got.Cron != "0 2 * * 1-5" ||
		got.Period != 0 || got.Grace != config.DefaultHeartbeatGrace {
		t.Fatalf("saved = %+v, %v", got, ok)
	}
	requireText(t, m, "Ping URL · nightly", "URL    "+hbBase+revealed(t, s, path, "nightly"))
}

func TestCronHeartbeatEditSwitchesScheduleAndKeepsTheToken(t *testing.T) {
	s, path, m := cronModel(t, 120, 40)
	token := revealed(t, s, path, "backup")
	m, _ = press(t, m, keyE)
	requireText(t, m, "Edit monitor", "Schedule: ‹ Period ›", "Period: 1h", "Token: ‹ keep ›")
	box := boxGeometry(t, m, "Edit monitor")
	m, _ = press(t, m, keyShiftTab, keyRight, keyTab)
	m = typeText(t, m, "0 2 * * 1-5")
	if got := boxGeometry(t, m, "Edit monitor"); got != box {
		t.Fatalf("Cron moved the box: %v, was %v:\n%s", got, box, view(m))
	}
	m, cmd := press(t, m, keyEnter)
	m = run(t, m, cmd)
	got, _ := storedByName(t, s, "backup")
	if got.Schedule != config.ScheduleCron || got.Cron != "0 2 * * 1-5" || got.Period != 0 || got.Grace != 10*time.Minute {
		t.Fatalf("saved = %+v", got)
	}
	if revealed(t, s, path, "backup") != token {
		t.Fatal("switching to cron replaced the token")
	}
	requireAbsent(t, m, "Ping URL ·", "uphb_")

	// Editing it again shows Cron; back to Period stores a period, no cron.
	m, _ = press(t, m, keyE)
	requireText(t, m, "Schedule: ‹ Cron ›", "Cron: 0 2 * * 1-5", "Next runs  Mon 26 02:00")
	requireAbsent(t, m, "Period:")
	m, _ = press(t, m, keyShiftTab, keyLeft)
	requireText(t, m, "Period: 24h")
	m, cmd = press(t, m, keyEnter)
	m = run(t, m, cmd)
	got, _ = storedByName(t, s, "backup")
	if got.Schedule != "" || got.Cron != "" || got.Period != 24*time.Hour {
		t.Fatalf("saved = %+v", got)
	}
	if revealed(t, s, path, "backup") != token {
		t.Fatal("switching back replaced the token")
	}
	_ = m
}

// cronHistoryModel has a cron heartbeat "nightly" (0 2 * * 1-5, grace 30m,
// zone -03) pinged on Tue, Thu and twice on Fri, with Wed's run missed.
func cronHistoryModel(t *testing.T, width, height int) Model {
	t.Helper()
	ctx := context.Background()
	s, path, m := cronModel(t, width, height)
	c := config.Check{Name: "nightly", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "0 2 * * 1-5", Grace: 30 * time.Minute}
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCheck(ctx, "backup"); err != nil {
		t.Fatal(err)
	}
	token := revealed(t, s, path, "nightly")
	ping := func(at time.Time) {
		t.Helper()
		if ok, err := s.PingHeartbeat(ctx, token, false, at); err != nil || !ok {
			t.Fatalf("ping = %v, %v", ok, err)
		}
	}
	day := func(d, h, min int) time.Time { return time.Date(2026, 10, d, h, min, 0, 0, time.UTC) }
	tue := day(20, 5, 1)
	ping(tue)
	loc, _ := config.LoadZone(cronZone)
	missed := check.Due(c, []check.Result{{CheckName: c.Name, At: tue, Status: check.StatusUp}}, day(21, 6, 0), loc)
	if len(missed) != 1 {
		t.Fatalf("due = %+v", missed)
	}
	if ok, err := s.RecordMissed(ctx, c, tue, missed[0]); err != nil || !ok {
		t.Fatalf("RecordMissed = %v, %v", ok, err)
	}
	ping(day(22, 5, 1))
	ping(day(23, 5, 1))
	ping(day(23, 5, 3))
	return reload(t, m)
}

func TestCronHeartbeatDetailAndRowUseRunsAndTheDefaultsZone(t *testing.T) {
	m := cronHistoryModel(t, 100, 40)
	requireRow(t, heartbeatRow(t, m, "nightly"), "UP", "▂█▂▂ ", "next by Mon 02:30 -03")
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "nightly  heartbeat · cron 0 2 * * 1-5 · grace 30m", "Next due     before Mon 02:30 -03 (in 1d 1h)",
		"PINGS  one cell per run, newest right", "▂░▂▂", "4 runs · 3 on time · ░ 1 missed · █ 0 failed",
		"Cron         0 2 * * 1-5", "Grace        30m", "no ping for the 02:00 -03 run")
	requireAbsent(t, m, "every ", "Period ", "▂░▂▂▂", "periods")
}

func TestCronHeartbeatAtEightyByTwentyFour(t *testing.T) {
	// The list's heartbeat rows were wider than 80 columns before cron (the
	// last column is not cut), so only the detail and the form are measured.
	m := cronHistoryModel(t, 80, 24)
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "nightly  heartbeat · cron 0 2 * * 1-5 · grace 30m", "before Mon 02:30 -03", "▂░▂▂")
	requireWidth(t, m, 80)
	if n := strings.Count(view(m), "\n") + 1; n > 24 {
		t.Fatalf("detail is %d lines:\n%s", n, view(m))
	}
	m, _ = press(t, m, keyE)
	requireText(t, m, "Edit monitor", "Schedule: ‹ Cron ›", "Cron: 0 2 * * 1-5", "Zone       "+cronZone+" (Defaults)",
		"Next runs  Mon 26 02:00 · Tue 27 02:00 · Wed 28 02:00")
	_ = boxGeometry(t, m, "Edit monitor")
	requireWidth(t, m, 80)
}
