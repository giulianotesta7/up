package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/config"
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

func TestFormModals(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 30}, {80, 24}, {120, 35}, {30, 8}} {
		for _, edit := range []bool{false, true} {
			m := modelWith(seededSnapshot(t, now))
			m.width, m.height = size.w, size.h
			before := view(m)
			key, title := keyN, "New monitor"
			if edit {
				key, title = keyE, "Edit monitor"
			}
			m, _ = press(t, m, key)
			out := view(m)
			for _, want := range []string{"Name", "URL", "tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"} {
				if !strings.Contains(out, want) {
					t.Errorf("%dx%d missing %q: %s", size.w, size.h, want, out)
				}
			}
			if size.w == 30 {
				if strings.Contains(out, "╭") || !strings.Contains(out, title) {
					t.Errorf("bad fallback: %s", out)
				}
			} else {
				for _, want := range []string{"payments", "╭─ " + title, "╰"} {
					if !strings.Contains(out, want) {
						t.Errorf("%dx%d missing %q: %s", size.w, size.h, want, out)
					}
				}
				for _, line := range strings.Split(out, "\n") {
					if ansi.StringWidth(line) > size.w {
						t.Errorf("line exceeds %d: %q", size.w, line)
					}
				}
				if size.w == 120 && !m.splitMode() {
					t.Error("form disabled split layout")
				}
			}
			m, _ = press(t, m, keyEsc)
			if view(m) != before {
				t.Errorf("escape did not restore view")
			}
		}
	}
}

func TestEditModalReturnsToDetail(t *testing.T) {
	m := modelWith(seededSnapshot(t, now))
	m.width, m.height = 100, 30
	m, _ = press(t, m, keyEnter)
	before := view(m)
	m, _ = press(t, m, keyE)
	if out := view(m); !strings.Contains(out, "╭─ Edit monitor") || !strings.Contains(out, "esc back") {
		t.Errorf("detail or popup missing: %s", out)
	}
	m, _ = press(t, m, keyEsc)
	if view(m) != before {
		t.Error("cancel did not restore detail")
	}
}

func TestSMTPModalStack(t *testing.T) {
	for _, size := range []struct{ w, h int }{{100, 30}, {120, 35}} {
		for _, child := range []struct {
			key   string
			title string
			field string
		}{{"n", "New SMTP server", "Password"}, {"e", "Edit SMTP server", "Host"}, {"d", "Delete SMTP server", "This cannot be undone."}} {
			snap := seededSnapshot(t, now)
			snap.HasKey = true
			snap.SMTPServers = []config.SMTPServer{{Name: "relay", Host: "smtp.example.com", Port: 587, TLS: config.TLSStartTLS}}
			m := modelWith(snap)
			m.width, m.height = size.w, size.h
			before := view(m)
			m, _ = press(t, m, keyS)
			parent := view(m)
			for _, want := range []string{"payments", "╭─ SMTP servers", "relay"} {
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
			for _, want := range []string{"╭─ SMTP servers", "╭─ " + child.title, child.field} {
				if !strings.Contains(out, want) {
					t.Errorf("%dx%d child missing %q: %s", size.w, size.h, want, out)
				}
			}
			for _, line := range strings.Split(m.View().Content, "\n") {
				if ansi.StringWidth(line) > size.w {
					t.Errorf("line exceeds %d: %q", size.w, line)
				}
			}
			if size.w == 120 && !m.splitMode() {
				t.Error("SMTP disabled split layout")
			}
			m, _ = press(t, m, keyEsc)
			if view(m) != parent {
				t.Error("child escape did not restore SMTP parent")
			}
			m, _ = press(t, m, keyEsc)
			if view(m) != before {
				t.Error("parent escape did not restore monitor view")
			}
		}
	}
}

func TestSMTPSmallFallback(t *testing.T) {
	snap := seededSnapshot(t, now)
	snap.SMTPServers = []config.SMTPServer{{Name: "relay", Host: "localhost", Port: 25, TLS: config.TLSNone}}
	m := modelWith(snap)
	m.width, m.height = 30, 8
	before := view(m)
	m, _ = press(t, m, keyS)
	parent := view(m)
	if strings.Contains(parent, "╭") || !strings.Contains(parent, "SMTP servers") || !strings.Contains(parent, "relay") {
		t.Errorf("parent fallback: %s", parent)
	}
	for _, tc := range []struct{ key, title string }{{"n", "New SMTP server"}, {"e", "Edit SMTP server"}, {"d", "Delete SMTP server"}} {
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

func TestSMTPModalMaskAndDetail(t *testing.T) {
	snap := seededSnapshot(t, now)
	snap.HasKey = true
	m := modelWith(snap)
	m.width, m.height = 100, 30
	m, _ = press(t, m, keyEnter)
	before := view(m)
	m, _ = press(t, m, keyS)
	parent := view(m)
	if !strings.Contains(parent, "╭─ SMTP servers") || !strings.Contains(parent, "esc back") {
		t.Errorf("detail background missing: %s", parent)
	}
	m, _ = press(t, m, keyN, keyTab, keyTab, keyTab, keyTab, keyTab)
	m = typeText(t, m, "sëcret")
	if out := view(m); strings.Contains(out, "sëcret") || !strings.Contains(out, "••••••") || !strings.Contains(out, "╭─ New SMTP server") {
		t.Errorf("password mask or modal missing: %s", out)
	}
	m, _ = press(t, m, keyEsc)
	if view(m) != parent {
		t.Error("child did not restore detail parent")
	}
	m, _ = press(t, m, keyEsc)
	if view(m) != before {
		t.Error("parent did not restore detail")
	}
}

func TestSMTPParentFallbackChildFits(t *testing.T) {
	snap := seededSnapshot(t, now)
	snap.HasKey = true
	m := modelWith(snap)
	m.width, m.height = 80, 24
	m, _ = press(t, m, keyS)
	parent := view(m)
	if strings.Contains(parent, "╭") || !strings.Contains(parent, "SMTP servers") {
		t.Fatalf("parent should fall back: %s", parent)
	}
	m, _ = press(t, m, keyN)
	if out := view(m); !strings.Contains(out, "╭─ New SMTP server") || !strings.Contains(out, "SMTP servers") {
		t.Errorf("fitting child over fallback parent: %s", out)
	}
	m, _ = press(t, m, keyEsc)
	if view(m) != parent {
		t.Error("escape did not restore fallback parent")
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
