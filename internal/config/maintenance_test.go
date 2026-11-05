package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

const (
	ny    = "America/New_York"
	tokyo = "Asia/Tokyo"
)

func window(zone string, startMinute int, d time.Duration, days ...time.Weekday) config.MaintenanceWindow {
	return config.MaintenanceWindow{Check: "api", Weekdays: days, StartMinute: startMinute, Duration: d, Zone: zone}
}

func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func TestEvaluateMaintenance(t *testing.T) {
	// 2026-03-08 (Sunday) is the New York spring-forward day: 02:00 EST
	// jumps to 03:00 EDT, so local 02:30 does not exist that day.
	// 2026-11-01 (Sunday) is the fall-back day: 02:00 EDT returns to 01:00
	// EST, so local 01:30 happens twice.
	tests := []struct {
		name    string
		windows []config.MaintenanceWindow
		at      time.Time
		active  bool
		end     time.Time
	}{
		{"no windows", nil, utc(2026, 3, 2, 12, 0), false, time.Time{}},
		{"inside", []config.MaintenanceWindow{window(ny, 9*60, 2*time.Hour, time.Monday)}, utc(2026, 3, 2, 15, 0), true, utc(2026, 3, 2, 16, 0)},
		{"start is inclusive", []config.MaintenanceWindow{window(ny, 9*60, time.Hour, time.Monday)}, utc(2026, 3, 2, 14, 0), true, utc(2026, 3, 2, 15, 0)},
		{"end is exclusive", []config.MaintenanceWindow{window(ny, 9*60, time.Hour, time.Monday)}, utc(2026, 3, 2, 15, 0), false, time.Time{}},
		{"before start", []config.MaintenanceWindow{window(ny, 9*60, time.Hour, time.Monday)}, utc(2026, 3, 2, 13, 59), false, time.Time{}},
		{"other weekday", []config.MaintenanceWindow{window(ny, 9*60, time.Hour, time.Tuesday)}, utc(2026, 3, 2, 14, 30), false, time.Time{}},
		{"multiple weekdays", []config.MaintenanceWindow{window(ny, 9*60, time.Hour, time.Monday, time.Wednesday)}, utc(2026, 3, 4, 14, 30), true, utc(2026, 3, 4, 15, 0)},
		{"unknown zone is ignored", []config.MaintenanceWindow{window("Nowhere/City", 0, time.Hour, time.Monday)}, utc(2026, 3, 2, 0, 30), false, time.Time{}},

		// Crossing midnight: Friday 23:00 for 2h also covers Saturday 00:30.
		{"midnight before", []config.MaintenanceWindow{window(ny, 23*60, 2*time.Hour, time.Friday)}, utc(2026, 3, 7, 4, 30), true, utc(2026, 3, 7, 6, 0)},
		{"midnight after", []config.MaintenanceWindow{window(ny, 23*60, 2*time.Hour, time.Friday)}, utc(2026, 3, 7, 5, 30), true, utc(2026, 3, 7, 6, 0)},
		{"midnight belongs to start weekday", []config.MaintenanceWindow{window(ny, 23*60, 2*time.Hour, time.Saturday)}, utc(2026, 3, 7, 5, 30), false, time.Time{}},
		{"multi-day duration", []config.MaintenanceWindow{window(ny, 12*60, 72*time.Hour, time.Monday)}, utc(2026, 3, 5, 5, 0), true, utc(2026, 3, 5, 17, 0)},

		// Spring-forward gap: the nonexistent 02:30 start is skipped.
		{"gap skipped", []config.MaintenanceWindow{window(ny, 2*60+30, time.Hour, time.Sunday)}, utc(2026, 3, 8, 7, 45), false, time.Time{}},
		{"gap skipped later that day", []config.MaintenanceWindow{window(ny, 2*60+30, time.Hour, time.Sunday)}, utc(2026, 3, 8, 8, 15), false, time.Time{}},
		{"gap week after runs", []config.MaintenanceWindow{window(ny, 2*60+30, time.Hour, time.Sunday)}, utc(2026, 3, 15, 6, 45), true, utc(2026, 3, 15, 7, 30)},
		{"gap week before runs", []config.MaintenanceWindow{window(ny, 2*60+30, time.Hour, time.Sunday)}, utc(2026, 3, 1, 7, 45), true, utc(2026, 3, 1, 8, 30)},
		{"start just before the gap runs elapsed", []config.MaintenanceWindow{window(ny, 60+30, 2*time.Hour, time.Sunday)}, utc(2026, 3, 8, 7, 45), true, utc(2026, 3, 8, 8, 30)},

		// Fall-back overlap: 01:30 runs once, at its first occurrence.
		{"overlap first occurrence", []config.MaintenanceWindow{window(ny, 60+30, time.Hour, time.Sunday)}, utc(2026, 11, 1, 5, 45), true, utc(2026, 11, 1, 6, 30)},
		{"overlap second occurrence not repeated", []config.MaintenanceWindow{window(ny, 60+30, time.Hour, time.Sunday)}, utc(2026, 11, 1, 6, 45), false, time.Time{}},
		{"overlap elapsed duration", []config.MaintenanceWindow{window(ny, 60+30, 2*time.Hour, time.Sunday)}, utc(2026, 11, 1, 7, 15), true, utc(2026, 11, 1, 7, 30)},
		{"overlap ended after elapsed duration", []config.MaintenanceWindow{window(ny, 60+30, 2*time.Hour, time.Sunday)}, utc(2026, 11, 1, 7, 30), false, time.Time{}},

		// Overlapping windows union; touching windows continue.
		{"overlap union", []config.MaintenanceWindow{window(ny, 9*60, time.Hour, time.Monday), window(ny, 9*60+30, time.Hour, time.Monday)}, utc(2026, 3, 2, 14, 15), true, utc(2026, 3, 2, 15, 30)},
		{"union reported from the later window", []config.MaintenanceWindow{window(ny, 9*60, time.Hour, time.Monday), window(ny, 9*60+30, time.Hour, time.Monday)}, utc(2026, 3, 2, 15, 15), true, utc(2026, 3, 2, 15, 30)},
		{"touching windows chain", []config.MaintenanceWindow{window(ny, 9*60, time.Hour, time.Monday), window(ny, 10*60, time.Hour, time.Monday), window(ny, 11*60, 30*time.Minute, time.Monday)}, utc(2026, 3, 2, 14, 30), true, utc(2026, 3, 2, 16, 30)},
		{"gap between windows ends the first", []config.MaintenanceWindow{window(ny, 9*60, time.Hour, time.Monday), window(ny, 10*60+1, time.Hour, time.Monday)}, utc(2026, 3, 2, 14, 30), true, utc(2026, 3, 2, 15, 0)},
		{"inner window does not shorten", []config.MaintenanceWindow{window(ny, 9*60, 3*time.Hour, time.Monday), window(ny, 10*60, 30*time.Minute, time.Monday)}, utc(2026, 3, 2, 15, 15), true, utc(2026, 3, 2, 17, 0)},

		// Multiple zones are evaluated independently at the same instant.
		{"tokyo only", []config.MaintenanceWindow{window(tokyo, 9*60, time.Hour, time.Monday), window(ny, 9*60, time.Hour, time.Monday)}, utc(2026, 3, 2, 0, 30), true, utc(2026, 3, 2, 1, 0)},
		{"new york only", []config.MaintenanceWindow{window(tokyo, 9*60, time.Hour, time.Monday), window(ny, 9*60, time.Hour, time.Monday)}, utc(2026, 3, 2, 14, 30), true, utc(2026, 3, 2, 15, 0)},
		{"weekday is local to the zone", []config.MaintenanceWindow{window(tokyo, 8*60, time.Hour, time.Monday)}, utc(2026, 3, 1, 23, 30), true, utc(2026, 3, 2, 0, 0)},
		{"utc zone", []config.MaintenanceWindow{window("UTC", 0, time.Hour, time.Monday)}, utc(2026, 3, 2, 0, 59), true, utc(2026, 3, 2, 1, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := config.EvaluateMaintenance(tt.windows, tt.at)
			if got.Active != tt.active || !got.End.Equal(tt.end) {
				t.Fatalf("EvaluateMaintenance(%s) = %+v, want active=%v end=%s", tt.at, got, tt.active, tt.end)
			}
			if got.Active && got.End.Location() != time.UTC {
				t.Fatalf("End location = %v, want UTC", got.End.Location())
			}
		})
	}
}

func TestEvaluateMaintenanceIsCappedWhenCoverageNeverEnds(t *testing.T) {
	always := config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{0, 1, 2, 3, 4, 5, 6}, Duration: 24 * time.Hour, Zone: "UTC"}
	at := utc(2026, 3, 2, 12, 0)
	got := config.EvaluateMaintenance([]config.MaintenanceWindow{always}, at)
	if !got.Active || !got.End.Equal(at.Add(config.MaintenanceHorizon)) {
		t.Fatalf("got %+v, want active until the horizon", got)
	}
}

func TestEvaluateMaintenanceDoesNotChangeInput(t *testing.T) {
	w := []config.MaintenanceWindow{window(ny, 9*60, time.Hour, time.Wednesday, time.Monday)}
	config.EvaluateMaintenance(w, utc(2026, 3, 2, 14, 30))
	if w[0].Weekdays[0] != time.Wednesday {
		t.Fatal("weekdays were reordered in place")
	}
}

func TestValidateMaintenanceWindow(t *testing.T) {
	valid := window(ny, 9*60, time.Hour, time.Monday)
	tests := []struct {
		name   string
		change func(*config.MaintenanceWindow)
		want   string
	}{
		{"valid", func(*config.MaintenanceWindow) {}, ""},
		{"shortest", func(w *config.MaintenanceWindow) { w.Duration = time.Minute }, ""},
		{"longest", func(w *config.MaintenanceWindow) { w.Duration = 7 * 24 * time.Hour }, ""},
		{"too short", func(w *config.MaintenanceWindow) { w.Duration = time.Minute - time.Second }, "duration must be between 1m and 7d"},
		{"too long", func(w *config.MaintenanceWindow) { w.Duration = 7*24*time.Hour + time.Second }, "duration must be between 1m and 7d"},
		{"no weekdays", func(w *config.MaintenanceWindow) { w.Weekdays = nil }, "at least one weekday is required"},
		{"weekday range", func(w *config.MaintenanceWindow) { w.Weekdays = []time.Weekday{7} }, "weekday 7 must be between 0 (Sunday) and 6 (Saturday)"},
		{"negative weekday", func(w *config.MaintenanceWindow) { w.Weekdays = []time.Weekday{-1} }, "weekday -1 must be between 0 (Sunday) and 6 (Saturday)"},
		{"duplicate weekday", func(w *config.MaintenanceWindow) { w.Weekdays = []time.Weekday{1, 1} }, "weekday 1 is repeated"},
		{"start before midnight", func(w *config.MaintenanceWindow) { w.StartMinute = -1 }, "start minute -1 must be between 0 and 1439"},
		{"start after last minute", func(w *config.MaintenanceWindow) { w.StartMinute = 1440 }, "start minute 1440 must be between 0 and 1439"},
		{"last minute", func(w *config.MaintenanceWindow) { w.StartMinute = 1439 }, ""},
		{"zone missing", func(w *config.MaintenanceWindow) { w.Zone = "" }, "zone is required"},
		{"zone unknown", func(w *config.MaintenanceWindow) { w.Zone = "Mars/Base" }, `zone "Mars/Base" is not an IANA time zone`},
		{"zone local", func(w *config.MaintenanceWindow) { w.Zone = "Local" }, `zone "Local" is not an IANA time zone`},
		{"check missing", func(w *config.MaintenanceWindow) { w.Check = "" }, "check is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := valid
			w.Weekdays = append([]time.Weekday(nil), valid.Weekdays...)
			tt.change(&w)
			err := config.ValidateMaintenanceWindow(w)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateMaintenanceWindow() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestWeekdayMask(t *testing.T) {
	m := config.WeekdayMask([]time.Weekday{time.Saturday, time.Monday})
	if m != 1<<6|1<<1 {
		t.Fatalf("mask = %b", m)
	}
	got := config.WeekdaysFromMask(m)
	if len(got) != 2 || got[0] != time.Monday || got[1] != time.Saturday {
		t.Fatalf("weekdays = %v", got)
	}
	if got := config.WeekdaysFromMask(0); got != nil {
		t.Fatalf("weekdays(0) = %v", got)
	}
}

func TestChannelTestStatuses(t *testing.T) {
	for _, s := range []config.ChannelTestStatus{config.TestPending, config.TestSending} {
		if !s.Active() {
			t.Errorf("%s should be active", s)
		}
	}
	for _, s := range []config.ChannelTestStatus{config.TestOK, config.TestError} {
		if s.Active() {
			t.Errorf("%s should be finished", s)
		}
	}
}
