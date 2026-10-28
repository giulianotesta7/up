package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	listFooter   = "↑/↓ select · enter open · n new · e edit · d delete · m mute · ? more · q quit"
	detailFooter = "esc back · e edit · d delete · m mute · ? more · q quit"
)

var keyHelp = tea.KeyPressMsg{Code: '?', Text: "?"}

func TestShortFooters(t *testing.T) {
	m := modelWith(seededSnapshot(t, now))
	requireText(t, m, listFooter)
	for _, text := range []string{"SMTP servers", "unmute", "animation", "ctrl+c quit"} {
		if strings.Contains(view(m), text) {
			t.Errorf("list footer still lists %q:\n%s", text, view(m))
		}
	}
	m, _ = press(t, m, keyEnter)
	requireText(t, m, detailFooter)
}

func TestFootersFitEightyColumns(t *testing.T) {
	for _, text := range []string{listFooter, detailFooter} {
		if w := ansi.StringWidth(text); w > 80 {
			t.Errorf("%q is %d columns", text, w)
		}
	}
}

func TestHelpOverlayListsEveryKey(t *testing.T) {
	m, _ := press(t, modelWith(seededSnapshot(t, now)), keyHelp)
	out := view(m)
	for _, want := range []string{"↑/↓ k/j", "enter", "new monitor", "edit", "delete", "mute", "unmute", "SMTP servers", "channels", "animation", "quit", "? or esc  close"} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "database: ") {
		t.Errorf("overlay lost the database footer:\n%s", out)
	}
}

func TestHelpOverlayClosesWithoutLeavingDetail(t *testing.T) {
	for _, closer := range []struct {
		name string
		key  tea.KeyPressMsg
	}{{"question", keyHelp}, {"esc", keyEsc}} {
		t.Run(closer.name, func(t *testing.T) {
			m, _ := press(t, modelWith(seededSnapshot(t, now)), keyEnter, keyHelp)
			m, _ = press(t, m, closer.key)
			if !m.detail || m.help {
				t.Fatalf("detail=%v help=%v, want detail kept and help closed", m.detail, m.help)
			}
			requireText(t, m, detailFooter)
		})
	}
}

func TestHelpOverlayIgnoresOtherKeys(t *testing.T) {
	m, _ := press(t, modelWith(seededSnapshot(t, now)), keyHelp)
	selected := m.selected
	m, _ = press(t, m, keyDown, keyN, keyD, keyEnter)
	if !m.help || m.selected != selected || m.form != nil || m.deletion != nil || m.detail {
		t.Fatalf("overlay leaked a key: %+v", m)
	}
	_, cmd := m.Update(keyCtrlC)
	if cmd == nil {
		t.Fatal("ctrl+c must still quit from the overlay")
	}
}

func TestHelpKeyOnlyOnListAndDetail(t *testing.T) {
	m := modelWith(seededSnapshot(t, now))
	for name, open := range map[string]func(Model) Model{
		"form":   func(m Model) Model { m, _ = press(t, m, keyN); return m },
		"smtp":   func(m Model) Model { m, _ = press(t, m, keyS); return m },
		"remove": func(m Model) Model { m, _ = press(t, m, keyD); return m },
	} {
		got, _ := press(t, open(m), keyHelp)
		if got.help {
			t.Errorf("%s: ? opened the overlay", name)
		}
	}
}
