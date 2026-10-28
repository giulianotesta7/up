package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/config"
)

func TestFormPopupWidthAcrossFocus(t *testing.T) {
	help := "←/→ choose · tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"
	cases := []struct {
		name, title string
		keys        []tea.KeyPressMsg
		count       int
	}{
		{"monitor-new", "New monitor", []tea.KeyPressMsg{keyN}, 9},
		{"monitor-edit", "Edit monitor", []tea.KeyPressMsg{{Code: 'e', Text: "e"}}, 8},
		{"smtp-new", "New SMTP server", []tea.KeyPressMsg{{Code: 's', Text: "s"}, keyN}, 7},
		{"smtp-edit", "Edit SMTP server", []tea.KeyPressMsg{{Code: 's', Text: "s"}, {Code: 'e', Text: "e"}}, 6},
		{"channel-new", "New channel", []tea.KeyPressMsg{{Code: 'c', Text: "c"}, keyN}, 4},
		{"channel-edit", "Edit channel", []tea.KeyPressMsg{{Code: 'c', Text: "c"}, {Code: 'e', Text: "e"}}, 3},
	}
	for _, tc := range cases {
		for _, width := range []int{160, ansi.StringWidth(help) + 6, ansi.StringWidth(help) + 5} {
			t.Run(tc.name+"/"+string(rune(width)), func(t *testing.T) {
				m := New(nil, func() time.Time { return now })
				m.snap = Snapshot{HasKey: true, Services: []Service{{Check: testChecks[0]}}, SMTPServers: []config.SMTPServer{{Name: "mail", Host: "host", Port: 587, TLS: config.TLSStartTLS}}, Channels: []config.Channel{{Name: "alerts", Kind: config.KindEmail, SMTPServer: "mail"}}}
				next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 50})
				m = next.(Model)
				m, _ = press(t, m, tc.keys...)
				var first string
				sawChoose, sawText := false, false
				for i := 0; i < tc.count; i++ {
					s := ansi.Strip(m.View().Content)
					if strings.Contains(s, help) {
						sawChoose = true
					} else if strings.Contains(s, "tab/shift+tab focus · enter save · esc cancel · ctrl+c quit") {
						sawText = true
					}
					border := "fallback"
					for _, line := range strings.Split(s, "\n") {
						if at := strings.Index(line, "╭─ "+tc.title+" "); at >= 0 {
							end := strings.Index(line[at:], "╮")
							if end >= 0 {
								border = string(rune(at)) + ":" + string(rune(ansi.StringWidth(line[at:at+end+len("╮")])))
							}
							break
						}
					}
					if i == 0 {
						first = border
					} else if border != first {
						t.Errorf("focus %d: border %q, initially %q", i, border, first)
					}
					m, _ = press(t, m, keyTab)
				}
				if !sawChoose || !sawText {
					t.Errorf("conditional help variants missing: choose=%v text=%v", sawChoose, sawText)
				}
			})
		}
	}
}

func modalBorder(t *testing.T, m Model, title string) (int, int) {
	t.Helper()
	for _, line := range strings.Split(view(m), "\n") {
		if i := strings.Index(line, "╭─ "+title+" "); i >= 0 {
			end := strings.Index(line[i:], "╮")
			return ansi.StringWidth(line[:i]), ansi.StringWidth(line[i : i+end+len("╮")])
		}
	}
	t.Fatalf("missing modal %s:\n%s", title, view(m))
	return 0, 0
}

func TestMutePopupWidthAcrossVariants(t *testing.T) {
	m := modelWith(seededSnapshot(t, now))
	m.width, m.height = 160, 50
	m, _ = press(t, m, keyM)
	left, width := modalBorder(t, m, "Mute alerts · api")
	m, _ = press(t, m, keyDown, keyDown, keyDown, keyDown, keyEnter)
	if !m.mute.custom {
		t.Fatal("did not enter custom duration")
	}
	l, w := modalBorder(t, m, "Mute alerts · api")
	if l != left || w != width {
		t.Fatalf("custom moved/resized popup: %d/%d -> %d/%d", left, width, l, w)
	}
	m, _ = press(t, m, keyEsc)
	l, w = modalBorder(t, m, "Mute alerts · api")
	if l != left || w != width {
		t.Fatal("return to presets moved popup")
	}
}

func TestMonitorPopupLongValueRemainsVisibleAcrossFocus(t *testing.T) {
	m := modelWith(seededSnapshot(t, now))
	m.width, m.height = 220, 50
	m, _ = press(t, m, keyN, keyTab)
	url := "https://example.com/" + strings.Repeat("segment", 12)
	m, _ = press(t, m, tea.KeyPressMsg{Text: url})
	left, width := modalBorder(t, m, "New monitor")
	if !strings.Contains(view(m), url) {
		t.Fatal("long URL was lost")
	}
	for i := 0; i < 9; i++ {
		m, _ = press(t, m, keyTab)
		l, w := modalBorder(t, m, "New monitor")
		if l != left || w != width {
			t.Fatalf("long value popup shifted at focus %d", i)
		}
		if !strings.Contains(view(m), url) {
			t.Fatal("long URL was truncated")
		}
	}
}
