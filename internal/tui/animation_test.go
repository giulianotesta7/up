package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

var keyA = tea.KeyPressMsg{Code: 'a', Text: "a"}

func TestAnimationToggle(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(map[bool]string{false: "list", true: "detail"}[detail], func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "xterm")
			snap := seededSnapshot(t, now.Add(-time.Second))
			m := modelWith(snap)
			if detail {
				m, _ = press(t, m, keyEnter)
			}
			flat := view(m)
			snap.LastBeat = now
			next, cmd := m.Update(snapshotMsg{snap: snap})
			m = next.(Model)
			if !strings.Contains(view(m), "╱╲╱") || cmd == nil {
				t.Fatal("animation must default on")
			}
			old := m.sweepGeneration
			m, cmd = press(t, m, keyA)
			if cmd != nil || view(m) != flat {
				t.Fatal("toggle off must immediately restore the flat view without a command")
			}
			if m.sweepGeneration == old {
				t.Fatal("toggle off must invalidate pending frames")
			}
			next, cmd = m.Update(sweepFrameMsg{generation: old})
			m = next.(Model)
			if cmd != nil || view(m) != flat {
				t.Fatal("pending frame changed disabled view or scheduled a command")
			}
			snap.LastBeat = now.Add(time.Second)
			next, cmd = m.Update(snapshotMsg{snap: snap})
			m = next.(Model)
			if m.sweeping || view(m) != flat || cmd == nil || m.sweepGeneration == old {
				t.Fatal("heartbeat while off must retain only polling and the flat view")
			}
			m, cmd = press(t, m, keyA)
			if cmd != nil || view(m) != flat {
				t.Fatal("toggle on must not start a sweep")
			}
			next, cmd = m.Update(sweepFrameMsg{generation: old})
			m = next.(Model)
			if cmd != nil || view(m) != flat {
				t.Fatal("toggle on revived a pending frame")
			}
			snap.LastBeat = now.Add(2 * time.Second)
			next, cmd = m.Update(snapshotMsg{snap: snap})
			m = next.(Model)
			if !strings.Contains(view(m), "╱╲╱") || cmd == nil {
				t.Fatal("next heartbeat must resume animation")
			}
			if batch, ok := cmd().(tea.BatchMsg); !ok || len(batch) != 2 {
				t.Fatal("resumed heartbeat must schedule polling and a frame")
			}
			next, cmd = m.Update(sweepFrameMsg{generation: old})
			if cmd != nil || view(next.(Model)) != view(m) {
				t.Fatal("old generation interfered with resumed sweep")
			}
		})
	}
}

func TestAnimationKeyPreservesModalInput(t *testing.T) {
	for _, mode := range []string{"new", "edit", "delete"} {
		t.Run(mode, func(t *testing.T) {
			s, m := managedModel(t, 1)
			before, err := Load(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "new":
				m, _ = press(t, m, keyN)
			case "edit":
				m, _ = press(t, m, keyE)
			case "delete":
				m, _ = press(t, m, keyD)
			}
			oldView, generation := view(m), m.sweepGeneration
			m, cmd := press(t, m, keyA)
			if cmd != nil || s.updates != 0 || s.deletes != 0 || m.sweepGeneration != generation {
				t.Fatal("modal a issued a command, wrote, or toggled animation")
			}
			if mode == "delete" {
				if view(m) != oldView {
					t.Fatal("a changed delete confirmation")
				}
			} else if view(m) != strings.Replace(oldView, "│", "a│", 1) {
				t.Fatal("a was not inserted at the focused field cursor")
			}
			after, err := Load(context.Background(), s)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("modal a changed stored data")
			}
		})
	}
}

func TestAnimationHelp(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	m := modelWith(seededSnapshot(t, now))
	for _, detail := range []bool{false, true} {
		if detail {
			m, _ = press(t, m, keyEnter)
		}
		prefix := "↑/↓ select (k/j) · enter details"
		if detail {
			prefix = "esc back"
		}
		want := prefix + " · n new monitor · e edit · d delete · m mute · u unmute · s SMTP servers · c channels · a animation · q quit · ctrl+c quit"
		if !strings.Contains(view(m), want) {
			t.Fatalf("missing complete help: %s", view(m))
		}
		if !strings.Contains(m.View().Content, accentStyle.Render("a")+" "+helpStyle.Render("animation")) {
			t.Fatal("animation key is not accented")
		}
	}
}
