package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestPopupOverScreen(t *testing.T) {
	for _, size := range []struct{ width, height int }{{120, 35}, {80, 24}} {
		m := modelWith(seededSnapshot(t, now))
		m.width, m.height = size.width, size.height
		before := view(m)
		m, _ = press(t, m, keyHelp)
		out := view(m)
		if !strings.Contains(out, "payments") || !strings.Contains(out, "╭─ keys") || !strings.Contains(out, "╰") {
			t.Errorf("%dx%d: missing underlying row or popup:\n%s", size.width, size.height, out)
		}
		if size.width == 120 && !m.splitMode() {
			t.Error("help disabled split layout")
		}
		for _, line := range strings.Split(out, "\n") {
			if ansi.StringWidth(line) > size.width {
				t.Errorf("line exceeds width %d: %q", size.width, line)
			}
			if i := strings.Index(line, "╭─ keys"); i >= 0 {
				left := ansi.StringWidth(line[:i])
				if left != (size.width-46)/2 {
					t.Errorf("popup left = %d, want %d", left, (size.width-46)/2)
				}
			}
		}
		for _, closer := range []string{"?", "esc"} {
			key := keyHelp
			if closer == "esc" {
				key = keyEsc
			}
			closed, _ := press(t, m, key)
			if view(closed) != before {
				t.Errorf("%s did not restore view", closer)
			}
		}
	}
}

func TestOverlayPreservesSurroundingANSI(t *testing.T) {
	background := "\x1b[31mabcdefghij\x1b[0m"
	out := overlayPopup(background, "BOX", 10, 1)
	if ansi.Strip(out) != "abcBOXghij" || !strings.Contains(out, "\x1b[31mabc") || !strings.Contains(out, "\x1b[31mghij") {
		t.Errorf("styled surroundings lost: %q", out)
	}
}

func TestPopupNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := modelWith(seededSnapshot(t, now))
	m.width, m.height = 80, 24
	m, _ = press(t, m, keyHelp)
	out := m.View().Content
	if strings.Contains(out, "\x1b[") || !strings.Contains(out, "╭─ keys") {
		t.Errorf("unexpected uncolored popup: %q", out)
	}
}

func TestPopupSmallFallback(t *testing.T) {
	m := modelWith(seededSnapshot(t, now))
	m.width, m.height = 30, 10
	m, _ = press(t, m, keyHelp)
	out := view(m)
	if strings.Contains(out, "╭") || !strings.Contains(out, "? or esc  close") || strings.Contains(out, "payments") {
		t.Errorf("unexpected fallback:\n%s", out)
	}
}

func TestKeyMapOmitsEnterInSplitMode(t *testing.T) {
	size := func(w, h int) Model {
		next, _ := modelWith(seededSnapshot(t, now)).Update(tea.WindowSizeMsg{Width: w, Height: h})
		m, _ := press(t, next.(Model), keyHelp)
		return m
	}
	if split := view(size(120, 35)); strings.Contains(split, "enter    details") {
		t.Errorf("split mode lists a key that does nothing:\n%s", split)
	}
	if !strings.Contains(view(size(120, 35)), "d        delete") {
		t.Error("split key map lost the other keys")
	}
	if plain := view(size(80, 24)); !strings.Contains(plain, "enter    details") {
		t.Errorf("unsplit key map must keep enter:\n%s", plain)
	}
}
