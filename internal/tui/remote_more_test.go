package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/config"
)

func remoteModel(t *testing.T, s *aggregateStore, width, height int) Model {
	t.Helper()
	m := New(s, time.Now, Options{Destination: "https://up.acme.com", DatabasePath: "/local/up.db"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	next, _ = next.(Model).Update(next.(Model).Init()())
	return next.(Model)
}

func localModel(t *testing.T, s *aggregateStore, width, height int) Model {
	t.Helper()
	m := New(s, time.Now, Options{DatabasePath: "/local/up.db"})
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	next, _ = next.(Model).Update(next.(Model).Init()())
	return next.(Model)
}

func seeded() *aggregateStore {
	return &aggregateStore{snap: Snapshot{Services: []Service{{Check: config.Check{Name: "api", URL: "https://api.example.com"}}}}}
}

func key(m Model, text string) Model {
	next, _ := m.Update(tea.KeyPressMsg{Code: rune(text[0]), Text: text})
	return next.(Model)
}

func fail(m Model, s *aggregateStore) Model {
	s.err = errors.New("offline")
	next, _ := m.Update(m.load())
	return next.(Model)
}

func TestRemoteHelpPopupAndKeyMapKeepTheirShape(t *testing.T) {
	for _, split := range []bool{false, true} {
		local, remote := ansi.Strip(renderHelpPopup(split)), ansi.Strip(renderHelpPopup(split, true))
		if strings.Contains(local, "retry") || !strings.Contains(remote, "retry") {
			t.Errorf("split=%v: retry must be listed only in remote mode\nlocal:\n%s\nremote:\n%s", split, local, remote)
		}
		if strings.Count(local, "\n") != strings.Count(remote, "\n") {
			t.Errorf("split=%v: the popup height changed between modes", split)
		}
		for i, line := range strings.Split(remote, "\n") {
			if ansi.StringWidth(line) != popupWidth {
				t.Errorf("split=%v: remote popup line %d is %d cells wide, want %d: %q", split, i, ansi.StringWidth(line), popupWidth, line)
			}
		}
	}
	local, remote := ansi.Strip(renderKeys()), ansi.Strip(renderKeys(true))
	if strings.Contains(local, "retry") || !strings.Contains(remote, "retry") || strings.Count(local, "\n") != strings.Count(remote, "\n") {
		t.Errorf("key map must list retry only in remote mode with the same height\nlocal:\n%s\nremote:\n%s", local, remote)
	}
}

func TestRemoteHelpOpensWithoutCrashing(t *testing.T) {
	for _, size := range [][2]int{{100, 30}, {40, 10}} {
		m := key(remoteModel(t, seeded(), size[0], size[1]), "?")
		if got := ansi.Strip(m.View().Content); !strings.Contains(got, "retry") {
			t.Errorf("%dx%d: the help must list retry:\n%s", size[0], size[1], got)
		}
		m = key(localModel(t, seeded(), size[0], size[1]), "?")
		if got := ansi.Strip(m.View().Content); strings.Contains(got, "retry") {
			t.Errorf("%dx%d: local help must not mention retry:\n%s", size[0], size[1], got)
		}
	}
}

func TestLoadErrorWordingOnEveryScreen(t *testing.T) {
	for name, open := range map[string]func(Model) Model{
		"list":    func(m Model) Model { return m },
		"detail":  func(m Model) Model { return key(m, "x") },
		"form":    func(m Model) Model { return key(m, "n") },
		"mute":    func(m Model) Model { return key(m, "m") },
		"delete":  func(m Model) Model { return key(m, "d") },
		"smtp":    func(m Model) Model { return key(m, "s") },
		"channel": func(m Model) Model { return key(m, "c") },
	} {
		for _, remote := range []bool{true, false} {
			s := seeded()
			var m Model
			if remote {
				m = remoteModel(t, s, 120, 40)
			} else {
				m = localModel(t, s, 120, 40)
			}
			if name == "detail" {
				next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				m = next.(Model)
			}
			m = fail(open(m), s)
			got := ansi.Strip(m.View().Content)
			want, unwanted := "error reading the database: offline", "cannot reach the server"
			if remote {
				want, unwanted = "cannot reach the server: offline", "error reading the database"
			}
			if !strings.Contains(got, want) || strings.Contains(got, unwanted) {
				t.Errorf("%s remote=%v: want %q and no %q:\n%s", name, remote, want, unwanted, got)
			}
		}
	}
}

func TestRemoteFooterFitsNarrowTerminals(t *testing.T) {
	for _, size := range [][2]int{{110, 30}, {80, 24}, {40, 12}, {30, 10}} {
		s := seeded()
		for label, m := range map[string]Model{"connected": remoteModel(t, s, size[0], size[1]), "disconnected": fail(remoteModel(t, s, size[0], size[1]), s)} {
			s.err = nil
			lines := strings.Split(ansi.Strip(m.View().Content), "\n")
			last := lines[len(lines)-1]
			if ansi.StringWidth(last) > size[0] || !strings.Contains(last, "●") {
				t.Errorf("%dx%d %s: footer %q does not fit or lacks the dot", size[0], size[1], label, last)
			}
			for i, line := range lines {
				if ansi.StringWidth(line) > size[0] && size[0] >= 40 && i == len(lines)-1 {
					t.Errorf("%dx%d %s: line %d is too wide", size[0], size[1], label, i)
				}
			}
		}
	}
}

func TestRetryIgnoresResultsOfOlderRequests(t *testing.T) {
	s := seeded()
	m := remoteModel(t, s, 100, 30)
	staleLoad := m.load // a request started before the retry
	m = key(m, "r")
	s.err = errors.New("late failure")
	next, _ := m.Update(staleLoad())
	if got := ansi.Strip(next.(Model).View().Content); strings.Contains(got, "late failure") || strings.Contains(got, "disconnected") {
		t.Errorf("a result from before the retry must be ignored:\n%s", got)
	}
}

func TestRetryNeverReloadsInLocalMode(t *testing.T) {
	s := seeded()
	m := localModel(t, s, 100, 30)
	before := s.calls
	next := key(m, "r")
	if s.calls != before {
		t.Errorf("r reloaded in local mode")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd != nil {
		t.Error("r must do nothing in local mode")
	}
	_ = next
}

func TestRetryDoesNotStealTypingInAnyInput(t *testing.T) {
	for name, open := range map[string][]string{
		"smtp form":    {"s", "n"},
		"channel form": {"c", "n"},
		"monitor form": {"n"},
	} {
		s := seeded()
		m := remoteModel(t, s, 120, 40)
		for _, k := range open {
			m = key(m, k)
		}
		before := s.calls
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
		if cmd != nil && s.calls != before {
			t.Errorf("%s: r triggered a reload while typing", name)
		}
		if next := key(m, "r"); next.generation != m.generation {
			t.Errorf("%s: r changed the request generation while typing", name)
		}
	}
}

func TestRemoteNeverShowsLocalData(t *testing.T) {
	// aggregateStore panics on any per-resource read (it embeds a nil Store),
	// so reaching a local read path would fail this test.
	s := seeded()
	m := remoteModel(t, s, 100, 30)
	m = fail(m, s)
	s.err = nil
	m = key(m, "r")
	next, _ := m.Update(m.load())
	if got := ansi.Strip(next.(Model).View().Content); !strings.Contains(got, "● remote · https://up.acme.com") || strings.Contains(got, "/local/up.db") {
		t.Errorf("remote footer must not mention the local database:\n%s", got)
	}
}
