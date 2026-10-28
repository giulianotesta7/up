package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/config"
)

func channelModalModel(t *testing.T, width, height int) Model {
	t.Helper()
	snap := seededSnapshot(t, now)
	snap.SMTPServers = []config.SMTPServer{{Name: "relay", Host: "localhost", Port: 25, TLS: config.TLSNone}}
	snap.Channels = []config.Channel{{Name: "ops", Kind: config.KindEmail, SMTPServer: "relay", Recipients: []string{"ops@example.com"}}}
	m := modelWith(snap)
	m.width, m.height = width, height
	return m
}

func TestChannelModalStack(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 30}, {120, 35}} {
		for _, child := range []struct{ key, title, field string }{{"n", "New channel", "Recipients"}, {"e", "Edit channel", "Name: ops"}, {"d", "Delete channel", "This cannot be undone."}} {
			m := channelModalModel(t, size.w, size.h)
			before := view(m)
			m, _ = press(t, m, keyC)
			parent := view(m)
			for _, want := range []string{"payments", "╭─ Channels", "ops@example.com"} {
				if !strings.Contains(parent, want) {
					t.Errorf("%dx%d parent missing %q: %s", size.w, size.h, want, parent)
				}
			}
			switch child.key {
			case "n":
				m, _ = press(t, m, keyN)
			case "e":
				m, _ = press(t, m, keyE)
			case "d":
				m, _ = press(t, m, keyD)
			}
			out := view(m)
			for _, want := range []string{"╭─ Channels", "╭─ " + child.title, child.field} {
				if !strings.Contains(out, want) {
					t.Errorf("%dx%d child missing %q: %s", size.w, size.h, want, out)
				}
			}
			for _, line := range strings.Split(m.View().Content, "\n") {
				if ansi.StringWidth(line) > size.w {
					t.Errorf("line exceeds width %d: %q", size.w, line)
				}
			}
			if size.w == 120 && !m.splitMode() {
				t.Error("channel disabled split")
			}
			m, _ = press(t, m, keyEsc)
			if view(m) != parent {
				t.Error("child escape did not restore parent")
			}
			m, _ = press(t, m, keyEsc)
			if view(m) != before {
				t.Error("parent escape did not restore monitor view")
			}
		}
	}
}

func TestChannelSmallFallback(t *testing.T) {
	m := channelModalModel(t, 30, 8)
	before := view(m)
	m, _ = press(t, m, keyC)
	parent := view(m)
	if strings.Contains(parent, "╭") || !strings.Contains(parent, "Channels") || !strings.Contains(parent, "ops@example.com") {
		t.Errorf("parent fallback: %s", parent)
	}
	for _, tc := range []struct{ key, title string }{{"n", "New channel"}, {"e", "Edit channel"}, {"d", "Delete channel"}} {
		child := m
		switch tc.key {
		case "n":
			child, _ = press(t, child, keyN)
		case "e":
			child, _ = press(t, child, keyE)
		case "d":
			child, _ = press(t, child, keyD)
		}
		if out := view(child); strings.Contains(out, "╭") || !strings.Contains(out, tc.title) {
			t.Errorf("child fallback: %s", out)
		}
		child, _ = press(t, child, keyEsc)
		if view(child) != parent {
			t.Error("fallback child did not close")
		}
	}
	m, _ = press(t, m, keyEsc)
	if view(m) != before {
		t.Error("fallback parent did not close")
	}
}

func TestChannelParentFallbackChildFits(t *testing.T) {
	m := channelModalModel(t, 80, 24)
	m, _ = press(t, m, keyC)
	parent := view(m)
	if strings.Contains(parent, "╭") || !strings.Contains(parent, "Channels") {
		t.Fatalf("parent should fall back: %s", parent)
	}
	m, _ = press(t, m, keyN)
	if out := view(m); !strings.Contains(out, "╭─ New channel") || !strings.Contains(out, "Channels") {
		t.Errorf("fitting child over fallback parent: %s", out)
	}
	m, _ = press(t, m, keyEsc)
	if view(m) != parent {
		t.Error("escape did not restore fallback parent")
	}
}

func TestChannelModalDetailReturn(t *testing.T) {
	m := channelModalModel(t, 100, 30)
	m, _ = press(t, m, keyEnter)
	before := view(m)
	m, _ = press(t, m, keyC)
	parent := view(m)
	if !strings.Contains(parent, "╭─ Channels") || !strings.Contains(parent, "esc back") {
		t.Errorf("detail background missing: %s", parent)
	}
	m, _ = press(t, m, keyN, keyEsc)
	if view(m) != parent {
		t.Error("child did not restore detail parent")
	}
	m, _ = press(t, m, keyEsc)
	if view(m) != before {
		t.Error("parent did not restore detail")
	}
}
