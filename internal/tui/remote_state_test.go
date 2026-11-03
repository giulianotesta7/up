package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/config"
)

func TestRemoteConnectionStatesAndRetry(t *testing.T) {
	s := &aggregateStore{snap: Snapshot{Services: []Service{{Check: config.Check{Name: "remote-monitor"}}}}}
	m := New(s, time.Now, Options{Destination: "https://example.com"})
	initial := ansi.Strip(m.View().Content)
	if !strings.HasSuffix(initial, "remote · https://example.com") || strings.HasSuffix(initial, "● remote · https://example.com") {
		t.Errorf("initial footer: %q", initial)
	}
	next, _ := m.Update(m.Init()())
	m = next.(Model)
	if got := ansi.Strip(m.View().Content); !strings.HasSuffix(got, "● remote · https://example.com") || !strings.Contains(got, "remote-monitor") {
		t.Errorf("connected: %q", got)
	}
	s.err = errors.New("offline")
	next, _ = m.Update(m.load())
	m = next.(Model)
	got := ansi.Strip(m.View().Content)
	if !strings.Contains(got, "cannot reach the server: offline") || !strings.Contains(got, "remote-monitor") || !strings.HasSuffix(got, "● disconnected · https://example.com — retrying (r retry · q quit)") {
		t.Errorf("disconnected: %q", got)
	}
	before := s.calls
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("retry did not schedule load")
	}
	s.err = nil
	next, _ = m.Update(cmd())
	m = next.(Model)
	if s.calls != before+1 || !strings.HasSuffix(ansi.Strip(m.View().Content), "● remote · https://example.com") {
		t.Errorf("retry: calls=%d view=%q", s.calls, ansi.Strip(m.View().Content))
	}
}

func TestRemoteRetryDoesNotStealFormInput(t *testing.T) {
	s := &aggregateStore{}
	m := New(s, time.Now, Options{Destination: "https://example.com"})
	next, _ := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	m = next.(Model)
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	m = next.(Model)
	if cmd != nil || s.calls != 0 || m.form == nil || m.form.values[0] != "r" {
		t.Fatalf("form retry: calls=%d form=%+v", s.calls, m.form)
	}
}
