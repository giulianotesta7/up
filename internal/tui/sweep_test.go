package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSweepRule(t *testing.T) {
	for _, width := range []int{0, 20, 60, 80, 10, 200} {
		cells := width
		if cells <= 0 {
			cells = 60
		}
		cells = min(80, max(20, cells))
		flat := accentStyle.Render(logoStem) + helpStyle.Render(strings.Repeat("─", cells-1))
		if got := renderRule(width, -1); got != flat {
			t.Fatalf("idle width %d: %q != %q", width, got, flat)
		}
		if got := renderHeader(Snapshot{}, width); got != renderTitle(Snapshot{})+"\n"+flat+"\n" {
			t.Fatal("idle header changed")
		}
		for _, pos := range []int{0, cells / 2, cells - 4, cells - 2, cells - 1, cells + 3} {
			got := renderRule(width, pos)
			pulse := []rune("╱╲╱")[:min(3, max(0, cells-1-pos))]
			want := "└" + strings.Repeat("─", min(pos, cells-1)) + string(pulse) + strings.Repeat("─", max(0, cells-1-pos-len(pulse)))
			if ansi.Strip(got) != want || ansi.StringWidth(got) != cells {
				t.Fatalf("width %d position %d: %q want %q (%d cells)", width, pos, got, want, cells)
			}
			if len(pulse) > 0 && !strings.Contains(got, accentStyle.Render(string(pulse))) {
				t.Fatal("pulse is not accented")
			}
		}
	}
}

func TestSweepEligibility(t *testing.T) {
	previous := Snapshot{HasBeat: true, LastBeat: now.Add(-time.Second)}
	fresh := Snapshot{HasBeat: true, LastBeat: now}
	for _, tc := range []struct {
		name           string
		previous, next Snapshot
		noColor, term  string
		want           bool
	}{
		{"new", previous, fresh, "", "xterm", true},
		{"first", Snapshot{}, fresh, "", "xterm", false},
		{"same", fresh, fresh, "", "xterm", false},
		{"older", fresh, previous, "", "xterm", false},
		{"stale", Snapshot{HasBeat: true, LastBeat: now.Add(-time.Hour)}, Snapshot{HasBeat: true, LastBeat: now.Add(-StaleAfter - time.Nanosecond)}, "", "xterm", false},
		{"threshold", Snapshot{HasBeat: true, LastBeat: now.Add(-time.Hour)}, Snapshot{HasBeat: true, LastBeat: now.Add(-StaleAfter)}, "", "xterm", true},
		{"no color", previous, fresh, "please", "xterm", false},
		{"dumb", previous, fresh, "", "dumb", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("TERM", tc.term)
			if got := shouldSweep(tc.previous, tc.next, now, tc.noColor, tc.term); got != tc.want {
				t.Fatalf("eligibility = %v", got)
			}
			m := New(nil, func() time.Time { return now })
			m.snap = tc.previous
			next, cmd := m.Update(snapshotMsg{snap: tc.next})
			m = next.(Model)
			if m.sweeping != tc.want || cmd == nil {
				t.Fatalf("sweeping=%v command=%v", m.sweeping, cmd != nil)
			}
			// A batch adds a frame command to the unchanged poll command; idle
			// returns only the existing poll tick (never execute timers in tests).
			if tc.want {
				if batch, ok := cmd().(tea.BatchMsg); !ok || len(batch) != 2 {
					t.Fatalf("start did not schedule poll + frame: %T", batch)
				}
			}
			if !tc.want && strings.Contains(view(m), "╱") {
				t.Fatal("disabled pulse visible")
			}
		})
	}
}

func TestSweepFramesAndRestart(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	m := modelWith(Snapshot{HasBeat: true, LastBeat: now.Add(-time.Second)})
	next, _ := m.Update(snapshotMsg{snap: Snapshot{HasBeat: true, LastBeat: now}})
	m = next.(Model)
	if !m.sweeping || m.sweepPosition != 0 {
		t.Fatal("did not start at zero")
	}
	old := m.sweepGeneration
	next, cmd := m.Update(sweepFrameMsg{generation: old})
	m = next.(Model)
	if m.sweepPosition != 2 || cmd == nil {
		t.Fatal("frame did not advance and schedule")
	}
	next, cmd = m.Update(snapshotMsg{snap: Snapshot{HasBeat: true, LastBeat: now.Add(time.Second)}})
	m = next.(Model)
	if m.sweepPosition != 0 || m.sweepGeneration == old || cmd == nil {
		t.Fatal("did not restart")
	}
	before := m
	next, cmd = m.Update(sweepFrameMsg{generation: old})
	m = next.(Model)
	if !reflect.DeepEqual(m.snap, before.snap) || m.sweepPosition != 0 || cmd != nil {
		t.Fatal("old frame not ignored")
	}
	for pos := 2; pos <= 60; pos += 2 {
		next, cmd = m.Update(sweepFrameMsg{generation: m.sweepGeneration})
		m = next.(Model)
		if pos < 59 {
			if m.sweepPosition != pos || !m.sweeping || cmd == nil {
				t.Fatalf("frame %d failed", pos)
			}
		} else if m.sweeping || cmd != nil {
			t.Fatal("last frame did not stop")
		}
	}
	if strings.Contains(view(m), "╱") {
		t.Fatal("finished view not flat")
	}
	next, cmd = m.Update(sweepFrameMsg{generation: m.sweepGeneration})
	if cmd != nil || next.(Model).sweeping {
		t.Fatal("idle frame restarted ticks")
	}
}

func TestSweepStopsWhenDisabled(t *testing.T) {
	for _, mode := range []string{"stale", "no color", "dumb"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "xterm")
			clock := now
			m := New(nil, func() time.Time { return clock })
			m.snap = Snapshot{HasBeat: true, LastBeat: now.Add(-time.Second)}
			next, _ := m.Update(snapshotMsg{snap: Snapshot{HasBeat: true, LastBeat: now}})
			m = next.(Model)
			switch mode {
			case "stale":
				clock = now.Add(StaleAfter + time.Second)
			case "no color":
				t.Setenv("NO_COLOR", "1")
			case "dumb":
				t.Setenv("TERM", "dumb")
			}
			if strings.Contains(view(m), "╱") {
				t.Fatal("disabled view not flat")
			}
			next, cmd := m.Update(sweepFrameMsg{generation: m.sweepGeneration})
			if next.(Model).sweeping || cmd != nil {
				t.Fatal("disabled sweep continued")
			}
		})
	}
}

func TestSweepPreservesKeysAndViews(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	idle := modelWith(seededSnapshot(t, now))
	for _, keys := range [][]tea.KeyPressMsg{{keyDown}, {keyDown, keyUp}, {keyEnter}, {keyEnter, keyEsc}, {keyN}, {keyE}, {keyD}, {keyQ}} {
		moving := idle
		moving.sweeping = true
		moving.sweepPosition = 4
		plain, pcmd := press(t, idle, keys...)
		animated, acmd := press(t, moving, keys...)
		if plain.selected != animated.selected || plain.detail != animated.detail || !reflect.DeepEqual(plain.form, animated.form) || !reflect.DeepEqual(plain.deletion, animated.deletion) || (pcmd == nil) != (acmd == nil) {
			t.Fatalf("keys %v changed behavior", keys)
		}
		p, a := strings.Split(view(plain), "\n"), strings.Split(view(animated), "\n")
		a[1] = p[1]
		if strings.Join(a, "\n") != strings.Join(p, "\n") {
			t.Fatalf("keys %v changed other view content", keys)
		}
		if pcmd != nil {
			if _, ok := acmd().(tea.QuitMsg); !ok {
				t.Fatal("quit lost")
			}
		}
	}
}
