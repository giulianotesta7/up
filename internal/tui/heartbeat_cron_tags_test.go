package tui

import (
	"reflect"
	"slices"
	"testing"

	"github.com/heohlee/up/internal/config"
)

// A cron heartbeat keeps the Tags row: tab goes Schedule, Cron, Grace, Tags,
// Channel, and saving stores the schedule and the tags together.
func TestCronHeartbeatKeepsTheTagsRow(t *testing.T) {
	s, _, m := cronModel(t, 120, 40)
	m = openCronForm(t, m)
	requireText(t, m, "Schedule: ‹ Cron ›", "Tags: ")
	if got, want := m.form.order(), []int{focusType, 0, focusSchedule, focusCron, focusGrace, focusTags, focusChannel, focusAlertAfter, focusRecoverAfter}; !slices.Equal(got, want) {
		t.Fatalf("cron heartbeat order = %v, want %v", got, want)
	}
	m, _ = press(t, m, keyShiftTab)
	m = typeText(t, m, "nightly")
	m, _ = press(t, m, keyTab, keyTab)
	m = typeText(t, m, "0 2 * * 1-5")
	m, _ = press(t, m, keyTab, keyTab)
	if m.form.focus != focusTags {
		t.Fatalf("focus after Cron and Grace = %d, want Tags", m.form.focus)
	}
	m = typeText(t, m, "Backup, nas")
	m, cmd := press(t, m, keyEnter)
	run(t, m, cmd)
	got, ok := storedByName(t, s, "nightly")
	if !ok || got.Schedule != config.ScheduleCron || got.Cron != "0 2 * * 1-5" || !reflect.DeepEqual(got.Tags, []string{"backup", "nas"}) {
		t.Fatalf("saved = %+v, %v", got, ok)
	}
	edit := editMonitorForm(got, false)
	if !edit.isCron() || edit.cron != "0 2 * * 1-5" || edit.tags != "backup, nas" {
		t.Fatalf("edit form = schedule %q cron %q tags %q", edit.schedule, edit.cron, edit.tags)
	}
}

// The box is measured over every Type and both schedules, so switching
// between them never moves it.
func TestFormVariantsCoverEveryKindAndBothSchedules(t *testing.T) {
	_, _, m := cronModel(t, 120, 40)
	m = openHeartbeatForm(t, m)
	type variant struct{ kind, schedule string }
	var got []variant
	for _, f := range formVariants(*m.form) {
		got = append(got, variant{f.kind, f.schedule})
	}
	want := []variant{{"", ""}, {kindTCP, ""}, {kindHeartbeat, ""}, {kindHeartbeat, config.ScheduleCron}, {kindPing, ""}, {kindDNS, ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("formVariants = %v, want %v", got, want)
	}
	box := boxGeometry(t, m, "New monitor")
	m, _ = press(t, m, keyTab, keyTab, keyRight) // Schedule ‹ Cron ›
	if got := boxGeometry(t, m, "New monitor"); got != box {
		t.Fatalf("Cron moved the box: %v, was %v:\n%s", got, box, view(m))
	}
	m, _ = press(t, m, keyShiftTab, keyShiftTab) // back to Type
	for range kindChoices {
		m, _ = press(t, m, keyRight)
		if got := boxGeometry(t, m, "New monitor"); got != box {
			t.Fatalf("Type %s moved the box: %v, was %v:\n%s", m.form.kindName(), got, box, view(m))
		}
	}
}
