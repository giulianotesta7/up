package tui

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// conditionModel is advancedModel with the fold open and the focus on
// + Condition.
func conditionModel(t *testing.T) Model {
	t.Helper()
	_, m := advancedModel(t)
	m, _ = press(t, m, keySpace)
	return tabs(t, m, 5)
}

var focusMarker = regexp.MustCompile(`(?:^|│ )> (.*)$`)

// conditionBlock is the text of the condition rows.
func conditionBlock(t *testing.T, m Model) string {
	t.Helper()
	v := view(m)
	start, end := strings.Index(v, "Condition 1:"), strings.Index(v, "+ Condition")
	if start < 0 || end < start {
		t.Fatalf("no condition rows:\n%s", v)
	}
	return v[start:end]
}

// focusedLine is the form line with the focus marker.
func focusedLine(t *testing.T, m Model) string {
	t.Helper()
	for _, line := range strings.Split(view(m), "\n") {
		if found := focusMarker.FindStringSubmatch(line); found != nil {
			return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(found[1]), "│"))
		}
	}
	t.Fatalf("no focused line:\n%s", view(m))
	return ""
}

func storedChecks(t *testing.T, m Model) []config.Check {
	t.Helper()
	checks, err := m.store.(*store.Store).Checks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return checks
}

func TestConditionRowAddCycleAndDelete(t *testing.T) {
	m := conditionModel(t)
	requireText(t, m, "+ Condition: space to add · all must pass")
	if strings.Contains(view(m), "Condition 1") {
		t.Fatalf("a condition exists before adding one:\n%s", view(m))
	}
	m, _ = press(t, m, keySpace)
	requireText(t, m, "Condition 1: ‹ JSON value ›", "Path: e.g. services.0.status", "Equals:", "←/→ kind · ctrl+d del")
	if got := focusedLine(t, m); !strings.HasPrefix(got, "Condition 1: ‹ JSON value ›") {
		t.Fatalf("focus after add = %q", got)
	}
	kinds := []struct {
		label  string
		fields []string
	}{
		{"Body regex", []string{"Pattern:"}},
		{"Max response time", []string{"At most: e.g. 800ms"}},
		{"Response header", []string{"Name:", "Equals:"}},
		{"JSON value", []string{"Path:", "Equals:"}},
	}
	all := []string{"Path:", "Pattern:", "At most:", "Name:", "Equals:"}
	for _, kind := range kinds {
		m, _ = press(t, m, keyRight)
		requireText(t, m, "Condition 1: ‹ "+kind.label+" ›")
		requireText(t, m, kind.fields...)
		for _, field := range all {
			if !strings.Contains(strings.Join(kind.fields, " "), field) && strings.Contains(conditionBlock(t, m), field) {
				t.Fatalf("%s shows %q:\n%s", kind.label, field, view(m))
			}
		}
	}
	m, _ = press(t, m, keyLeft)
	requireText(t, m, "Condition 1: ‹ Response header ›")

	// A second row, then ctrl+d on a field of the first removes the first.
	m, _ = press(t, m, keyTab, keyTab, keyTab, keySpace)
	requireText(t, m, "Condition 2: ‹ JSON value ›")
	m, _ = press(t, m, keyShiftTab)
	if got := focusedLine(t, m); !strings.HasPrefix(got, "Equals:") {
		t.Fatalf("focus = %q", got)
	}
	m, _ = press(t, m, keyCtrlD)
	requireText(t, m, "Condition 1: ‹ JSON value ›")
	if strings.Contains(view(m), "Condition 2") || strings.Contains(view(m), "Response header") {
		t.Fatalf("condition not removed:\n%s", view(m))
	}
	if got := focusedLine(t, m); !strings.HasPrefix(got, "Condition 1:") {
		t.Fatalf("focus after delete = %q", got)
	}
	m, _ = press(t, m, keyCtrlD)
	if strings.Contains(view(m), "Condition 1") {
		t.Fatalf("condition not removed:\n%s", view(m))
	}
	if got := focusedLine(t, m); !strings.HasPrefix(got, "+ Condition") {
		t.Fatalf("focus after last delete = %q", got)
	}
}

func TestConditionsSaveToTheStore(t *testing.T) {
	m := conditionModel(t)
	m, _ = press(t, m, keySpace, keyTab)
	m = typeText(t, m, "services.0.status")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "ok")
	m, _ = press(t, m, keyTab, keySpace, keyRight, keyTab)
	m = typeText(t, m, "(?i)ready")
	m, _ = press(t, m, keyTab, keySpace, keyRight, keyRight, keyTab)
	m = typeText(t, m, "800ms")
	m, _ = press(t, m, keyTab, keySpace, keyLeft, keyTab)
	m = typeText(t, m, "X-Version")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "3")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	checks := storedChecks(t, m)
	want := []config.Condition{
		{Kind: config.ConditionJSON, Path: "services.0.status", Value: "ok"},
		{Kind: config.ConditionRegex, Pattern: "(?i)ready"},
		{Kind: config.ConditionResponseTime, Within: 800 * time.Millisecond},
		{Kind: config.ConditionHeader, Name: "X-Version", Value: "3"},
	}
	if len(checks) != 1 || !reflect.DeepEqual(checks[0].Conditions, want) {
		t.Fatalf("stored %+v", checks)
	}
}

func TestConditionKindChangeKeepsOnlyItsFields(t *testing.T) {
	m := conditionModel(t)
	m, _ = press(t, m, keySpace, keyTab)
	m = typeText(t, m, "status")
	m, _ = press(t, m, keyShiftTab, keyLeft, keyTab)
	m = typeText(t, m, "X-Version")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	want := []config.Condition{{Kind: config.ConditionHeader, Name: "X-Version"}}
	if checks := storedChecks(t, m); len(checks) != 1 || !reflect.DeepEqual(checks[0].Conditions, want) {
		t.Fatalf("stored %+v", checks)
	}
}

// Text typed under every kind stays in the row while cycling, but saving sends
// only the chosen kind's fields, so the save is valid.
func TestConditionCycledThroughEveryKindSavesOnlyTheChosenOne(t *testing.T) {
	m := conditionModel(t)
	m, _ = press(t, m, keySpace, keyTab)
	m = typeText(t, m, "status")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "ok")
	m, _ = press(t, m, keyShiftTab, keyShiftTab, keyRight, keyTab)
	m = typeText(t, m, "ready")
	m, _ = press(t, m, keyShiftTab, keyRight, keyTab)
	m = typeText(t, m, "800ms")
	m, _ = press(t, m, keyShiftTab, keyRight, keyTab)
	m = typeText(t, m, "X-Version")
	m, _ = press(t, m, keyShiftTab, keyLeft, keyLeft)
	requireText(t, m, "Condition 1: ‹ Body regex ›", "Pattern: ready")
	m, cmd := press(t, m, keyEnter)
	if cmd == nil {
		t.Fatalf("save refused:\n%s", view(m))
	}
	m = finishCommand(t, m, cmd)
	want := []config.Condition{{Kind: config.ConditionRegex, Pattern: "ready"}}
	if checks := storedChecks(t, m); len(checks) != 1 || !reflect.DeepEqual(checks[0].Conditions, want) {
		t.Fatalf("stored %+v", checks)
	}
}

func TestConditionSaveErrorFocusesItsRow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, m Model) Model
		want  string
		focus string
	}{
		{"bad regex", func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keySpace, keyRight, keyTab)
			return typeText(t, m, "(ready|ok")
		}, `Cannot save monitor: condition 2: body regex "(ready|ok" is not valid: missing closing )`, "Pattern: (ready|ok"},
		{"bad JSON path", func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keySpace, keyTab)
			m = typeText(t, m, "checks..db")
			m, _ = press(t, m, keyTab)
			return typeText(t, m, "true")
		}, `Cannot save monitor: condition 2: JSON path "checks..db" is not valid: use names and array indexes separated by dots, like services.0.status`, "Path: checks..db"},
		{"duration without unit", func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keySpace, keyRight, keyRight, keyTab)
			return typeText(t, m, "800")
		}, `Cannot save monitor: condition 2: max response time "800" is not valid: use a duration like 800ms`, "At most: 800"},
		{"limit not under the timeout", func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keySpace, keyRight, keyRight, keyTab)
			return typeText(t, m, "5s")
		}, "Cannot save monitor: condition 2: max response time (5s) must be lower than timeout (5s)", "At most: 5s"},
		{"limit not whole milliseconds", func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keySpace, keyRight, keyRight, keyTab)
			return typeText(t, m, "1500us")
		}, "Cannot save monitor: condition 2: max response time must be a whole number of milliseconds", "At most: 1500us"},
		{"missing header name", func(t *testing.T, m Model) Model {
			m, _ = press(t, m, keySpace, keyLeft, keyTab, keyTab)
			return typeText(t, m, "3")
		}, "Cannot save monitor: condition 2: header name is required", "Name:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := conditionModel(t)
			m, _ = press(t, m, keySpace, keyTab)
			m = typeText(t, m, "status")
			m, _ = press(t, m, keyTab)
			m = typeText(t, m, "ok")
			m, _ = press(t, m, keyTab)
			m = tc.build(t, m)
			m, _ = press(t, m, keyTab, keyTab, keyTab) // away from the row
			m, cmd := press(t, m, keyEnter)
			if cmd != nil {
				t.Fatal("invalid condition was saved")
			}
			requireText(t, m, tc.want)
			if got := focusedLine(t, m); !strings.HasPrefix(got, tc.focus) {
				t.Fatalf("focus = %q, want %q:\n%s", got, tc.focus, view(m))
			}
			if checks := storedChecks(t, m); len(checks) != 0 {
				t.Fatalf("stored %+v", checks)
			}
		})
	}
}

func TestAdvancedClosedHasNoConditions(t *testing.T) {
	s, m := formStore(t)
	m = fillForm(t, m, "api", "http://example.com", "30s", "5s", "200")
	if strings.Contains(view(m), "Condition") {
		t.Fatalf("closed fold shows conditions:\n%s", view(m))
	}
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	checks, err := s.Checks(context.Background())
	if err != nil || len(checks) != 1 || checks[0].Conditions != nil {
		t.Fatalf("checks %+v %v", checks, err)
	}
}

var storedConditions = []config.Condition{
	{Kind: config.ConditionJSON, Path: "status", Value: "ok"},
	{Kind: config.ConditionResponseTime, Within: 800 * time.Millisecond},
}

// conditionStore is a real store with one HTTP monitor with conditions.
func conditionStore(t *testing.T) (*store.Store, Model) {
	t.Helper()
	s, m := formStore(t)
	c := config.Check{Name: "api", URL: "https://example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200, Conditions: storedConditions}
	if err := s.AddCheck(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	return s, next.(Model)
}

func TestEditAndDuplicateReopenConditions(t *testing.T) {
	s, m := conditionStore(t)
	m, _ = press(t, m, keyE)
	requireText(t, m, "▾ Advanced  configured", "Condition 1: ‹ JSON value ›", "Path: status", "Equals: ok", "Condition 2: ‹ Max response time ›", "At most: 800ms")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	if got, _ := storedByName(t, s, "api"); !reflect.DeepEqual(got.Conditions, storedConditions) {
		t.Fatalf("edit changed conditions: %+v", got.Conditions)
	}
	m, _ = press(t, m, keyY)
	requireText(t, m, "Condition 1: ‹ JSON value ›", "At most: 800ms")
	m, cmd = press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	if got, ok := storedByName(t, s, "api copy"); !ok || !reflect.DeepEqual(got.Conditions, storedConditions) {
		t.Fatalf("copy conditions: %+v %v", got.Conditions, ok)
	}
}

func TestEditRemovesAllConditions(t *testing.T) {
	s, m := conditionStore(t)
	m, _ = press(t, m, keyE)
	m = tabs(t, m, 13) // from the URL to Condition 1
	if got := focusedLine(t, m); !strings.HasPrefix(got, "Condition 1:") {
		t.Fatalf("focus = %q:\n%s", got, view(m))
	}
	m, _ = press(t, m, keyCtrlD, keyCtrlD)
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	if got, _ := storedByName(t, s, "api"); got.Conditions != nil {
		t.Fatalf("conditions left: %+v", got.Conditions)
	}
}

func TestDetailListsConditionsTersely(t *testing.T) {
	c := config.Check{Name: "api", URL: "https://example.com", Interval: 30e9, Timeout: 5e9, ExpectedStatus: 200, Conditions: []config.Condition{
		{Kind: config.ConditionJSON, Path: "status", Value: "ok"},
		{Kind: config.ConditionJSON, Path: "checks.db.healthy", Value: "true"},
		{Kind: config.ConditionResponseTime, Within: 800 * time.Millisecond},
		{Kind: config.ConditionHeader, Name: "Authorization", Value: "Bearer s3cret"},
		{Kind: config.ConditionRegex, Pattern: strings.Repeat("x", 200)},
	}}
	out := ansi.Strip(renderDetail(Snapshot{}, Service{Check: c}, now, nil, 80))
	for _, want := range []string{
		"  Expected     200\n  Expect       JSON status = \"ok\"\n",
		"\n               JSON checks.db.healthy = \"true\"\n",
		"\n               response within 800ms\n",
		"\n               header Authorization = ••••\n",
		"\n               body matches /xxx",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "s3cret") {
		t.Fatalf("sensitive value shown:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("line wider than 80: %q", line)
		}
	}
	c.Conditions = nil
	if plain := ansi.Strip(renderDetail(Snapshot{}, Service{Check: c}, now, nil, 80)); strings.Contains(plain, "Expect ") {
		t.Fatalf("plain detail lists conditions:\n%s", plain)
	}
}

func TestConditionFormFitsAt80x24(t *testing.T) {
	m := conditionModel(t)
	m.width, m.height = 80, 24
	m, _ = press(t, m, keySpace, keyTab)
	m = typeText(t, m, "status")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "ok")
	m, _ = press(t, m, keyTab, keySpace, keyRight, keyRight)
	out := view(m)
	lines := strings.Split(out, "\n")
	if len(lines) > 24 {
		t.Fatalf("%d lines exceed 24:\n%s", len(lines), out)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("line wider than 80: %q\n%s", line, out)
		}
	}
	for _, want := range []string{"╭─ New monitor", "╰", "↑ ", " more", "Condition 2: ‹ Max response time ›", "At most", "←/→ kind · ctrl+d del", "esc cancel"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}
