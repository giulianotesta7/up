package tui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// keySpace is shared with form_advanced_test.go.
var keyW = tea.KeyPressMsg{Code: 'w', Text: "w"}

// maintenanceModel has the check "api" and a loaded snapshot. now is a Sunday
// 03:45 UTC.
func maintenanceModel(t *testing.T, windows ...config.MaintenanceWindow) (*store.Store, Model) {
	t.Helper()
	s, m := formStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, testChecks[0]); err != nil {
		t.Fatal(err)
	}
	for _, w := range windows {
		w.Check = "api"
		if _, err := s.AddMaintenanceWindow(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Beat(ctx, now); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	return s, next.(Model)
}

func windowsOf(t *testing.T, s *store.Store) []config.MaintenanceWindow {
	t.Helper()
	ws, err := s.MaintenanceWindows(context.Background(), "api")
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

var (
	sundayNight = config.MaintenanceWindow{Weekdays: []time.Weekday{time.Sunday}, StartMinute: 3 * 60, Duration: 2 * time.Hour, Zone: "UTC"}
	lateNight   = config.MaintenanceWindow{Weekdays: []time.Weekday{time.Monday, time.Friday}, StartMinute: 23 * 60, Duration: 2 * time.Hour, Zone: "America/New_York"}
)

func TestMaintenancePopupListsWindows(t *testing.T) {
	_, m := maintenanceModel(t, lateNight, sundayNight)
	m, _ = press(t, m, keyW)
	requireText(t, m, "Maintenance · api",
		"Mon, Fri 23:00 + 2h · America/New_York · crosses midnight",
		"Sun 03:00 + 2h · UTC",
		"Alerts are held during a window or while muted. Checks and uptime keep counting.",
		"↑/↓ choose · a add · e edit · d delete · esc close")
	if strings.Contains(view(m), "Sun 03:00 + 2h · UTC · crosses") {
		t.Fatalf("a window inside the day is not crossing midnight:\n%s", view(m))
	}
	m, _ = press(t, m, keyEsc)
	if strings.Contains(view(m), "Maintenance · api") {
		t.Fatal("esc did not close the popup")
	}
}

func TestMaintenancePopupWithoutWindows(t *testing.T) {
	_, m := maintenanceModel(t)
	m, _ = press(t, m, keyW)
	requireText(t, m, "No windows yet. Press a to add one.")
}

func TestMaintenanceAddWindow(t *testing.T) {
	s, m := maintenanceModel(t)
	m, _ = press(t, m, keyW, keyA)
	// The zone defaults to the viewer's, and the start and duration to 02:00 and 1h.
	requireText(t, m, "Add window", "02:00", "1h", "UTC")
	// Monday and Friday: the day cursor starts on Monday.
	m, _ = press(t, m, keySpace, keyRight, keyRight, keyRight, keyRight, keySpace)
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "02:00", "23:00")
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "1h", "2h")
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "UTC", "America/New_York")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	want := []config.MaintenanceWindow{{ID: 1, Check: "api", Weekdays: []time.Weekday{time.Monday, time.Friday}, StartMinute: 23 * 60, Duration: 2 * time.Hour, Zone: "America/New_York"}}
	if got := windowsOf(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("stored %+v, want %+v", got, want)
	}
	requireText(t, m, "Mon, Fri 23:00 + 2h · America/New_York · crosses midnight")
}

func TestMaintenanceRejectedWindowsStoreNothingAndShowWhy(t *testing.T) {
	cases := []struct {
		name, start, duration, zone string
		days                        bool
		want                        string
	}{
		{"no weekday", "02:00", "1h", "UTC", false, "at least one weekday is required"},
		{"short duration", "02:00", "30s", "UTC", true, "duration must be between 1m and 7d"},
		{"long duration", "02:00", "200h", "UTC", true, "duration must be between 1m and 7d"},
		{"bad duration", "02:00", "soon", "UTC", true, `duration "soon" is not a duration (use values like 45m or 2h)`},
		{"bad start", "25:00", "1h", "UTC", true, `start "25:00" must be a 24-hour time like 23:00`},
		{"bad zone", "02:00", "1h", "Mars/Base", true, `zone "Mars/Base" is not an IANA time zone`},
		{"local zone", "02:00", "1h", "Local", true, `zone "Local" is not an IANA time zone`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, m := maintenanceModel(t)
			m, _ = press(t, m, keyW, keyA)
			if tc.days {
				m, _ = press(t, m, keySpace)
			}
			m, _ = press(t, m, keyTab)
			m = replaceField(t, m, "02:00", tc.start)
			m, _ = press(t, m, keyTab)
			m = replaceField(t, m, "1h", tc.duration)
			m, _ = press(t, m, keyTab)
			m = replaceField(t, m, "UTC", tc.zone)
			m, cmd := press(t, m, keyEnter)
			if cmd != nil {
				t.Fatal("an invalid window was submitted")
			}
			requireText(t, m, "Cannot save: ", tc.want, "Add window")
			if got := windowsOf(t, s); len(got) != 0 {
				t.Fatalf("stored %+v", got)
			}
		})
	}
}

func TestMaintenanceEditAndDeleteWindow(t *testing.T) {
	s, m := maintenanceModel(t, lateNight, sundayNight)
	// The list is in store order; edit the first.
	m, _ = press(t, m, keyW, keyE)
	requireText(t, m, "Edit window", "23:00", "2h", "America/New_York")
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "23:00", "22:30")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	got := windowsOf(t, s)
	if len(got) != 2 || got[0].StartMinute != 22*60+30 || got[0].ID != 1 || got[0].Zone != "America/New_York" {
		t.Fatalf("edit stored %+v", got)
	}
	// Esc from the confirmation deletes nothing; y deletes the selected one.
	m, _ = press(t, m, keyDown, keyD)
	requireText(t, m, "Delete the window Sun 03:00 + 2h · UTC?", "y delete · n cancel")
	m, _ = press(t, m, keyEsc)
	if len(windowsOf(t, s)) != 2 {
		t.Fatal("cancelled delete removed a window")
	}
	m, _ = press(t, m, keyD)
	m, cmd = press(t, m, keyY)
	finishCommand(t, m, cmd)
	if got := windowsOf(t, s); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("after delete %+v", got)
	}
}

func TestMaintenanceActiveWindowShownInListAndDetail(t *testing.T) {
	_, m := maintenanceModel(t, sundayNight)
	list := view(m)
	if !strings.Contains(list, "HELD") || !strings.Contains(list, "◷ until 05:00") || strings.Contains(list, "⏸") {
		t.Fatalf("list lacks the window or shows a mute:\n%s", list)
	}
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "Window", "until 05:00 (1h 15m left)", "nothing pending", "Windows", "1 window (w to edit)")
	if strings.Contains(view(m), "Muted") {
		t.Fatalf("detail shows a mute:\n%s", view(m))
	}
	m, _ = press(t, m, keyW)
	requireText(t, m, "Maintenance active until 05:00 (1h 15m left)")
}

func TestMaintenanceAndMuteShownTogether(t *testing.T) {
	s, m := maintenanceModel(t, sundayNight)
	if err := s.SetMute(context.Background(), "api", now.Add(4*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	list := view(m)
	if !strings.Contains(list, "◷ until 05:00 · ⏸ 4h left") {
		t.Fatalf("list lacks both:\n%s", list)
	}
	m, _ = press(t, m, keyW)
	requireText(t, m, "Maintenance active until 05:00 (1h 15m left) · Muted until 07:45 (4h left)")
}

func TestMaintenanceListWithoutActiveWindowKeepsMutedColumn(t *testing.T) {
	_, m := maintenanceModel(t, lateNight)
	list := view(m)
	if !strings.Contains(list, "MUTED") || strings.Contains(list, "HELD") || strings.Contains(list, "◷") {
		t.Fatalf("an inactive window changed the list:\n%s", list)
	}
}

func TestMaintenanceKeyDoesNotChangeMuteKeysAndIsInHelp(t *testing.T) {
	_, m := mutedModel(t, check.StatusUp, check.StatusDown)
	m, _ = press(t, m, keyM)
	requireText(t, m, "Mute alerts · api")
	m, _ = press(t, m, keyEsc, keyQ)
	m, _ = press(t, m, keyU)
	m, _ = press(t, m, tea.KeyPressMsg{Code: '?', Text: "?"})
	requireText(t, m, "w", "maintenance")
}

// A mute started inside a window starts from what the recipients were told,
// however many results the window holds back.
func TestMuteInsideWindowStartsFromWhatRecipientsKnow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		known bool
	}{{"reported down", true}, {"nothing reported", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := maintenanceModel(t, sundayNight)
			ctx := context.Background()
			for i := 0; i < 80; i++ {
				r := check.Result{CheckName: "api", At: at("03:10:00").Add(time.Duration(i) * 20 * time.Second), Status: check.StatusDown, Latency: time.Millisecond}
				if err := s.Record(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.SetAlertState(ctx, "api", tc.known); err != nil {
				t.Fatal(err)
			}
			next, _ := m.Update(m.Init()())
			m = next.(Model)
			if got := m.snap.Services[0].KnownAlerting; got != tc.known {
				t.Fatalf("snapshot KnownAlerting = %v, want %v", got, tc.known)
			}
			m, cmd := press(t, m, keyM, keyEnter)
			finishCommand(t, m, cmd)
			mute, ok, err := s.Mute(ctx, "api")
			if err != nil || !ok || mute.WasAlerting != tc.known {
				t.Fatalf("mute = %+v %v %v, want WasAlerting %v", mute, ok, err, tc.known)
			}
		})
	}
}

func TestMaintenanceDetailPendingUsesRecordedState(t *testing.T) {
	s, m := maintenanceModel(t, sundayNight)
	ctx := context.Background()
	if err := s.Record(ctx, check.Result{CheckName: "api", At: at("03:10:00"), Status: check.StatusDown}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlertState(ctx, "api", true); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m, _ = press(t, next.(Model), keyEnter)
	requireText(t, m, "nothing pending")
	if err := s.SetAlertState(ctx, "api", false); err != nil {
		t.Fatal(err)
	}
	next, _ = m.Update(m.Init()())
	requireText(t, next.(Model), "1 alert pending (down since")
}

// The popup list scrolls: with the maximum of windows the selected row, the
// header and the footer stay on screen at 80x24.
func TestMaintenancePopupScrollsAtTheMaximumOfWindows(t *testing.T) {
	var windows []config.MaintenanceWindow
	for i := 0; i < store.MaxMaintenanceWindows; i++ {
		w := sundayNight
		w.StartMinute = i * 10
		windows = append(windows, w)
	}
	_, m := maintenanceModel(t, windows...)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)
	m, _ = press(t, m, keyW)
	verify := func(selected int) {
		t.Helper()
		out := view(m)
		row := fmt.Sprintf("Sun %02d:%02d + 2h · UTC", selected*10/60, selected*10%60)
		for _, want := range []string{"Maintenance · api", "Checks and uptime keep counting", row, "↑/↓ choose · a add · e edit · d delete · esc close"} {
			if !strings.Contains(out, want) {
				t.Fatalf("selected %d: missing %q:\n%s", selected, want, out)
			}
		}
		if lines := strings.Count(out, "\n") + 1; lines > 24 {
			t.Fatalf("selected %d: %d lines at height 24:\n%s", selected, lines, out)
		}
	}
	verify(0)
	for i := 1; i < store.MaxMaintenanceWindows; i++ {
		m, _ = press(t, m, keyDown)
		verify(i)
	}
	// The delete confirmation adds rows and must still fit.
	m, _ = press(t, m, keyD)
	out := view(m)
	if !strings.Contains(out, "y delete · n cancel") || strings.Count(out, "\n")+1 > 24 {
		t.Fatalf("confirmation does not fit:\n%s", out)
	}
}

// plainStore hides the optional capabilities of the store it wraps.
type plainStore struct{ Store }

func TestMaintenanceWithoutWriterShowsAnError(t *testing.T) {
	s, _ := maintenanceModel(t)
	m := New(plainStore{s}, func() time.Time { return now })
	next, _ := m.Update(m.Init()())
	m, _ = press(t, next.(Model), keyW, keyA, keySpace)
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "Cannot save: this server does not support maintenance windows", "Add window")
	if got := windowsOf(t, s); len(got) != 0 {
		t.Fatalf("stored %+v", got)
	}
}

func TestShortDurationMatchesWhatMuteAccepts(t *testing.T) {
	for d, want := range map[time.Duration]string{2 * time.Hour: "2h", 90 * time.Minute: "1h30m", 45 * time.Minute: "45m", 36 * time.Hour: "36h", time.Minute: "1m"} {
		if got := shortDuration(d); got != want {
			t.Errorf("shortDuration(%v) = %q, want %q", d, got, want)
		}
		if back, err := time.ParseDuration(shortDuration(d)); err != nil || back != d {
			t.Errorf("%q does not round trip: %v %v", shortDuration(d), back, err)
		}
	}
}

// Without a recorded state, recipients can only know a down that had a channel
// to be sent to.
func TestKnownAlertingFallbackNeedsAChannel(t *testing.T) {
	for _, channel := range []string{"", "mail"} {
		t.Run("channel="+channel, func(t *testing.T) {
			s, m := maintenanceModel(t)
			ctx := context.Background()
			if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "smtp", Host: "example.com", Port: 25, TLS: config.TLSNone, Sender: "a@example.com"}); err != nil {
				t.Fatal(err)
			}
			if err := s.AddChannel(ctx, config.Channel{Name: "mail", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
				t.Fatal(err)
			}
			c := testChecks[0]
			c.Channel = channel
			if err := s.UpdateCheck(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := s.Record(ctx, check.Result{CheckName: "api", At: at("03:10:00"), Status: check.StatusDown}); err != nil {
				t.Fatal(err)
			}
			if _, found, err := s.AlertState(ctx, "api"); err != nil || found {
				t.Fatalf("alert state found = %v, %v; want none", found, err)
			}
			next, _ := m.Update(m.Init()())
			m = next.(Model)
			if got, want := m.snap.Services[0].KnownAlerting, channel != ""; got != want {
				t.Fatalf("KnownAlerting = %v, want %v", got, want)
			}
		})
	}
}
