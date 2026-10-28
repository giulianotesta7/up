package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderModal(t *testing.T) {
	box, ok := renderModal("Title", "short", 42, 5)
	if !ok || ansi.StringWidth(strings.Split(box, "\n")[0]) != 40 || !strings.Contains(ansi.Strip(box), "╭─ Title ") {
		t.Errorf("box %q ok %v", box, ok)
	}
	for _, size := range []struct{ w, h int }{{41, 5}, {42, 4}} {
		if _, ok := renderModal("Title", "short", size.w, size.h); ok {
			t.Errorf("unexpected fit: %+v", size)
		}
	}
}

func TestModalScreens(t *testing.T) {
	for _, size := range []struct{ w, h int }{{120, 35}, {80, 24}, {0, 0}, {30, 8}} {
		for _, tc := range []struct {
			name string
			mute bool
		}{{"Delete monitor", false}, {"Mute alerts · api", true}} {
			m := modelWith(seededSnapshot(t, now))
			m.width, m.height = size.w, size.h
			before := view(m)
			if tc.mute {
				m, _ = press(t, m, keyM)
			} else {
				m, _ = press(t, m, keyD)
			}
			out := view(m)
			if size.w == 30 {
				if strings.Contains(out, "╭") || !strings.Contains(out, tc.name) {
					t.Errorf("fallback: %s", out)
				}
			} else {
				for _, want := range []string{"payments", "╭─ " + tc.name, "╰"} {
					if !strings.Contains(out, want) {
						t.Errorf("%dx%d missing %q: %s", size.w, size.h, want, out)
					}
				}
				if size.w == 120 && !m.splitMode() {
					t.Error("split disabled")
				}
				if size.w > 0 {
					for _, line := range strings.Split(out, "\n") {
						if ansi.StringWidth(line) > size.w {
							t.Errorf("line exceeds %d: %q", size.w, line)
						}
					}
				}
			}
			closers := []string{"esc"}
			if !tc.mute {
				closers = append(closers, "n")
			}
			for _, closer := range closers {
				key := keyEsc
				if closer == "n" {
					key = keyN
				}
				closed, _ := press(t, m, key)
				if view(closed) != before {
					t.Errorf("%s did not restore view", closer)
				}
			}
		}
	}
}
