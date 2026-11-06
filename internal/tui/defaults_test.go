package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

var keyF = tea.KeyPressMsg{Code: 'f', Text: "f"}

// defaultsModel is formStore with the SMTP server "s" and the given channels.
func defaultsModel(t *testing.T, channels ...string) (*store.Store, Model) {
	t.Helper()
	s, m := formStore(t)
	ctx := context.Background()
	if len(channels) > 0 {
		if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "s", Host: "smtp.example.com", Port: 587, TLS: config.TLSStartTLS, Username: "u", Sender: "a@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range channels {
		if err := s.AddChannel(ctx, config.Channel{Name: name, Kind: config.KindEmail, SMTPServer: "s", Recipients: []string{"b@example.com"}}); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := m.Update(m.Init()())
	return s, next.(Model)
}

func storedDefaults(t *testing.T, s *store.Store) config.Defaults {
	t.Helper()
	d, err := s.Defaults(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDefaultsPopupShowsGroupsAndBuiltinValues(t *testing.T) {
	_, m := defaultsModel(t)
	m, _ = press(t, m, keyF)
	requireText(t, m, "Defaults", "New items only; existing ones keep their values.",
		"NEW MAINTENANCE WINDOWS", "IANA zone: UTC", "Also the time in heartbeat reasons.",
		"NEW MONITORS", "Channel: ‹ none ›", "Add a channel with c on the list",
		"Interval: 30s", "Timeout: 5s", "Expected status: 200",
		"Alert after: ‹ 1 › failures in a row", "Recover after: ‹ 1 › successes in a row",
		"tab/shift+tab focus · enter save · esc cancel")
}

func TestDefaultsPopupOmitsNoChannelNoteWhenChannelsExist(t *testing.T) {
	_, m := defaultsModel(t, "ops")
	m, _ = press(t, m, keyF)
	requireText(t, m, "Channel: ‹ none ›")
	if strings.Contains(view(m), "Add a channel with c on the list") {
		t.Fatalf("note shown although a channel exists:\n%s", view(m))
	}
}

func TestDefaultsSaveStoresValuesAndLeavesMonitorsAlone(t *testing.T) {
	s, m := defaultsModel(t, "ops")
	if err := s.AddCheck(context.Background(), testChecks[0]); err != nil {
		t.Fatal(err)
	}
	m, _ = press(t, m, keyF)
	m = replaceField(t, m, "UTC", "America/Argentina/Buenos_Aires")
	m, _ = press(t, m, keyTab, keyRight) // channel -> ops
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "30s", "1m")
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "5s", "10s")
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "200", "204")
	m, _ = press(t, m, keyTab, keyRight, keyRight) // alert after -> 3
	m, _ = press(t, m, keyTab, keyRight)           // recover after -> 2
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	want := config.Defaults{Zone: "America/Argentina/Buenos_Aires", Channel: "ops", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204, AlertAfter: 3, RecoverAfter: 2}
	if got := storedDefaults(t, s); got != want {
		t.Fatalf("stored %+v, want %+v", got, want)
	}
	requireText(t, m, "✓ Defaults saved")
	if m.defaults != nil || strings.Contains(view(m), "NEW MONITORS") {
		t.Fatalf("popup still open after save:\n%s", view(m))
	}
	checks, _ := s.Checks(context.Background())
	if len(checks) != 1 || !checks[0].SameDefinition(testChecks[0]) {
		t.Fatalf("saving defaults changed monitors: %+v", checks)
	}
	// The confirmation goes away with the next key.
	m, _ = press(t, m, keyJ)
	if strings.Contains(view(m), "Defaults saved") {
		t.Fatalf("confirmation stayed:\n%s", view(m))
	}
}

func TestDefaultsRejectedValuesStoreNothingAndShowTheValidatorMessage(t *testing.T) {
	cases := []struct {
		name                         string
		zone, interval, timeout, any string
		want                         string
	}{
		{"timeout above interval", "UTC", "5s", "10s", "200", "Cannot save defaults: timeout (10s) must not exceed interval (5s)"},
		{"zone", "Mars/Base", "30s", "5s", "200", `Cannot save defaults: zone "Mars/Base" is not an IANA time zone`},
		{"local zone", "Local", "30s", "5s", "200", `Cannot save defaults: zone "Local" is not an IANA time zone`},
		{"interval text", "UTC", "soon", "5s", "200", "Cannot save defaults: interval: time: invalid duration"},
		{"status", "UTC", "30s", "5s", "700", "Cannot save defaults: expected status 700 must be between 100 and 599"},
		{"status text", "UTC", "30s", "5s", "ok", "Cannot save defaults: expected status:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, m := defaultsModel(t)
			m, _ = press(t, m, keyF)
			m = replaceField(t, m, "UTC", tc.zone)
			m, _ = press(t, m, keyTab, keyTab)
			m = replaceField(t, m, "30s", tc.interval)
			m, _ = press(t, m, keyTab)
			m = replaceField(t, m, "5s", tc.timeout)
			m, _ = press(t, m, keyTab)
			m = replaceField(t, m, "200", tc.any)
			m, cmd := press(t, m, keyEnter)
			if cmd != nil {
				t.Fatal("invalid defaults were submitted")
			}
			requireText(t, m, tc.want, "NEW MONITORS")
			if got := storedDefaults(t, s); got != config.BuiltinDefaults() {
				t.Fatalf("stored %+v", got)
			}
		})
	}
}

func TestDefaultsEscCancelsWithoutSaving(t *testing.T) {
	s, m := defaultsModel(t)
	m, _ = press(t, m, keyF)
	m = replaceField(t, m, "UTC", "Europe/Madrid")
	m, cmd := press(t, m, keyEsc)
	if cmd != nil || m.defaults != nil {
		t.Fatalf("esc did not close the popup: %+v", m.defaults)
	}
	if got := storedDefaults(t, s); got != config.BuiltinDefaults() {
		t.Fatalf("stored %+v", got)
	}
	m, _ = press(t, m, keyF)
	requireText(t, m, "IANA zone: UTC")
}

func TestDefaultsPopupStartsFromStoredValues(t *testing.T) {
	s, m := defaultsModel(t, "ops")
	d := config.Defaults{Zone: "Europe/Madrid", Channel: "ops", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204, AlertAfter: 3, RecoverAfter: 2}
	if err := s.SetDefaults(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m, _ = press(t, next.(Model), keyF)
	requireText(t, m, "IANA zone: Europe/Madrid", "Channel: ‹ ops ›", "Interval: 1m0s", "Timeout: 10s", "Expected status: 204",
		"Alert after: ‹ 3 ›", "Recover after: ‹ 2 ›", "Detection time ≈ 3 × 1m0s = 3m0s before the first alert")
}

func TestDefaultsFitEightyByTwentyFour(t *testing.T) {
	_, m := defaultsModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = press(t, next.(Model), keyF)
	out := view(m)
	for _, want := range []string{"Defaults", "NEW MAINTENANCE WINDOWS", "Recover after", "enter save"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q at 80x24:\n%s", want, out)
		}
	}
	lines := strings.Split(out, "\n")
	if len(lines) > 24 {
		t.Fatalf("%d lines at 80x24:\n%s", len(lines), out)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("line wider than 80: %q", line)
		}
	}
}

func TestDefaultsWithoutAWriterSaysSo(t *testing.T) {
	m := modelWith(Snapshot{})
	m, _ = press(t, m, keyF)
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "Cannot save defaults: this server does not support defaults", "NEW MONITORS")
}

func TestHelpListsDefaultsKey(t *testing.T) {
	m, _ := press(t, modelWith(seededSnapshot(t, now)), keyHelp)
	requireText(t, m, "f   defaults")
	m, _ = press(t, m, keyEsc)
	requireText(t, m, listFooter)
	if strings.Contains(listFooter, "defaults") {
		t.Fatal("the list footer is full; f belongs in the help")
	}
}

func TestNewMonitorFormStartsFromDefaults(t *testing.T) {
	s, m := defaultsModel(t, "ops")
	d := config.Defaults{Zone: "Europe/Madrid", Channel: "ops", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204, AlertAfter: 3, RecoverAfter: 2}
	if err := s.SetDefaults(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)

	// HTTP: every default is prefilled and saved with the monitor.
	m, _ = press(t, m, keyN)
	requireText(t, m, "Interval: 1m0s", "Timeout: 10s", "Expected status: 204", "Channel: ‹ ops ›", "Alert after: ‹ 3 ›", "Recover after: ‹ 2 ›")
	m = typeText(t, m, "web")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "https://example.com")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	checks, _ := s.Checks(context.Background())
	if len(checks) != 1 || checks[0].Interval != time.Minute || checks[0].Timeout != 10*time.Second || checks[0].ExpectedStatus != 204 || checks[0].Channel != "ops" || checks[0].AlertAfter != 3 || checks[0].RecoverAfter != 2 {
		t.Fatalf("saved monitor %+v ignores the defaults", checks)
	}

	// TCP: the shared fields start from the defaults too; the status is HTTP only.
	m, _ = press(t, next.(Model), keyN, keyShiftTab, keyRight)
	requireText(t, m, "Type: ‹ TCP ›", "Interval: 1m0s", "Timeout: 10s", "Channel: ‹ ops ›", "Alert after: ‹ 3 ›", "Recover after: ‹ 2 ›")
	// Switching back to HTTP restores the default status, not a hard-coded 200.
	m, _ = press(t, next.(Model), keyN, keyShiftTab, keyRight, keyLeft)
	requireText(t, m, "Type: ‹ HTTP ›", "Expected status: 204")
}

func TestNewMonitorFormIgnoresADeletedDefaultChannel(t *testing.T) {
	snap := Snapshot{Channels: []config.Channel{{Name: "other"}}, Defaults: config.Defaults{Zone: "UTC", Channel: "gone", Interval: time.Minute, Timeout: 5 * time.Second, ExpectedStatus: 200, AlertAfter: 1, RecoverAfter: 1}}
	m, _ := press(t, modelWith(snap), keyN)
	requireText(t, m, "Channel: ‹ none ›", "Interval: 1m0s")
}

func TestNewMonitorFormWithoutStoredDefaultsKeepsToday(t *testing.T) {
	_, m := defaultsModel(t)
	m, _ = press(t, m, keyN)
	requireText(t, m, "Interval: 30s", "Timeout: 5s", "Expected status: 200", "Channel: ‹ none ›", "Alert after: ‹ 1 ›", "Recover after: ‹ 1 ›")
	if got := newMonitorForm(Snapshot{}); got.values != [5]string{"", "", "30s", "5s", "200"} || got.channel != "" || got.alertAfter != "1" || got.recoverAfter != "1" {
		t.Fatalf("newMonitorForm(zero snapshot) = %+v", got)
	}
}

func TestEditingAMonitorIgnoresDefaults(t *testing.T) {
	s, m := defaultsModel(t, "ops")
	ctx := context.Background()
	if err := s.AddCheck(ctx, testChecks[1]); err != nil {
		t.Fatal(err)
	}
	d := config.Defaults{Zone: "Europe/Madrid", Channel: "ops", Interval: 2 * time.Minute, Timeout: 20 * time.Second, ExpectedStatus: 301, AlertAfter: 5, RecoverAfter: 5}
	if err := s.SetDefaults(ctx, d); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m, _ = press(t, next.(Model), keyE)
	requireText(t, m, "Edit monitor", "Interval: 1m0s", "Timeout: 10s", "Expected status: 204", "Channel: ‹ none ›", "Alert after: ‹ 1 ›", "Recover after: ‹ 1 ›")
}

func TestNewMaintenanceWindowStartsWithTheDefaultZone(t *testing.T) {
	s, m := maintenanceModel(t)
	d := config.BuiltinDefaults()
	d.Zone = "Asia/Tokyo"
	if err := s.SetDefaults(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	base := next.(Model)
	m, _ = press(t, base, keyW, keyA)
	requireText(t, m, "Add maintenance window", "IANA zone", "Asia/Tokyo")
	// Editing an existing window keeps its own zone.
	if _, err := s.AddMaintenanceWindow(context.Background(), config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{time.Monday}, Duration: time.Hour, Zone: "America/New_York"}); err != nil {
		t.Fatal(err)
	}
	next, _ = base.Update(base.Init()())
	m, _ = press(t, next.(Model), keyW, keyE)
	requireText(t, m, "America/New_York")
}

func TestNewMaintenanceWindowIgnoresTheViewersZone(t *testing.T) {
	t.Setenv("TZ", "Asia/Tokyo")
	_, m := maintenanceModel(t)
	m, _ = press(t, m, keyW, keyA)
	requireText(t, m, "UTC")
	if strings.Contains(view(m), "Asia/Tokyo") {
		t.Fatalf("the viewer's zone leaked into the form:\n%s", view(m))
	}
}
