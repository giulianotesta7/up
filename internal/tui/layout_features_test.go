package tui

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// popupScreens are the popups and forms the five features added or made
// taller. Each builds a model whose screen is open.
func popupScreens(t *testing.T) map[string]Model {
	t.Helper()
	screens := map[string]Model{}
	{
		_, m := formStore(t)
		screens["new HTTP"], _ = press(t, m, keyN)
	}
	{
		_, m := advancedModel(t)
		screens["new HTTP, Advanced open"], _ = press(t, m, keySpace)
	}
	{
		_, m := advancedModel(t)
		m, _ = press(t, m, keySpace, keyTab, keyRight, keyRight, keyTab, keySpace)
		m = typeText(t, m, "X-Token")
		m, _ = press(t, m, keyTab)
		m = typeText(t, m, "hunter2")
		m, cmd := press(t, m, keyEnter)
		m = finishCommand(t, m, cmd)
		screens["edit configured"], _ = press(t, m, keyE)
	}
	screens["new TCP"] = openTCPForm(t)
	{
		_, m := smtpSetup(t)
		m, _ = press(t, m, keyC, keyN)
		m = typeText(t, m, "ops-hook")
		m, _ = press(t, m, keyTab, keyRight, keyTab)
		screens["webhook form"] = typeText(t, m, "https://hooks.example.com/x")
	}
	{
		_, m := testChannelModel(t, true)
		screens["channel list"] = m
		screens["test confirm"], _ = press(t, m, keyT)
	}
	{
		s, m := testChannelModel(t, true)
		m, _ = press(t, m, keyT)
		m, cmd := press(t, m, keyEnter)
		m = finishCommand(t, m, cmd)
		claimed, _, _ := s.ClaimChannelTest(context.Background(), now)
		long := "535 5.7.8 authentication failed: username and password not accepted by smtp.example.com"
		if err := s.CompleteChannelTest(context.Background(), claimed.ID, long, now); err != nil {
			t.Fatal(err)
		}
		screens["test failed with a long error"] = reload(t, m)
	}
	{
		_, m := maintenanceModel(t, lateNight, sundayNight)
		m, _ = press(t, m, keyW)
		screens["maintenance"] = m
		screens["maintenance add"], _ = press(t, m, keyA)
	}
	return screens
}

// Every popup keeps the title row ("▲ up …") on top and fits the terminal in
// width and height at the two reference sizes. The monitor detail is excluded
// from the height check: its chart section is taller than 24 rows by design.
func TestFeaturePopupsKeepTitleAndFitTerminal(t *testing.T) {
	for name, model := range popupScreens(t) {
		for _, size := range []struct{ w, h int }{{80, 24}, {140, 40}} {
			next, _ := model.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
			out := view(next.(Model))
			lines := strings.Split(out, "\n")
			if !strings.HasPrefix(lines[0], "▲ up") {
				t.Errorf("%s at %dx%d: title row covered: %q", name, size.w, size.h, lines[0])
			}
			if len(lines) > size.h {
				t.Errorf("%s at %dx%d: %d rows:\n%s", name, size.w, size.h, len(lines), out)
			}
			for i, line := range lines {
				if ansi.StringWidth(line) > size.w {
					t.Errorf("%s at %dx%d: row %d is %d cells wide: %q", name, size.w, size.h, i+1, ansi.StringWidth(line), line)
				}
			}
		}
	}
}

// A held cell is not cut by the split layout's list column.
func TestSplitListKeepsHeldCellWhole(t *testing.T) {
	_, m := maintenanceModel(t, sundayNight)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = next.(Model)
	if !m.splitMode() {
		t.Fatal("140x40 should use the split layout")
	}
	out := view(m)
	if !strings.Contains(out, "◷ until 05:00") {
		t.Fatalf("held cell cut:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "◷") && !strings.Contains(line, "│") {
			t.Fatalf("detail pane lost beside the held cell: %q", line)
		}
	}
}

// Every line of the channel-test popup has the same width in the failed state,
// so the right border stays aligned whatever the error text.
func TestFailedChannelTestPopupKeepsBorderAligned(t *testing.T) {
	model := popupScreens(t)["test failed with a long error"]
	for _, w := range []int{80, 140} {
		next, _ := model.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		var widths []int
		in := false
		for _, line := range strings.Split(ansi.Strip(view(next.(Model))), "\n") {
			if strings.Contains(line, "╭─ Test channel") {
				in = true
			}
			if !in {
				continue
			}
			start := strings.IndexAny(line, "╭│╰")
			end := strings.LastIndexAny(line, "╮│╯")
			if start < 0 || end < start {
				break
			}
			_, size := utf8.DecodeRuneInString(line[end:])
			widths = append(widths, ansi.StringWidth(line[start:end+size]))
			if strings.Contains(line, "╯") {
				break
			}
		}
		if len(widths) < 5 {
			t.Fatalf("popup not found at width %d: %v", w, widths)
		}
		for _, x := range widths {
			if x != widths[0] {
				t.Fatalf("popup lines differ in width at %d: %v", w, widths)
			}
		}
	}
}
