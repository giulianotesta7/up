package tui

import (
	"bytes"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestIdentityViews(t *testing.T) {
	m := modelWith(seededSnapshot(t, now))
	m.databasePath = "/shared/up.db"
	// Previous fresh fixture line counts: list 9, detail 14, form 10, delete 6.
	// The list's blank becomes the rule; other views retain their title below it.
	for _, tc := range []struct {
		name  string
		key   tea.KeyPressMsg
		lines int
		help  string
	}{
		{"list", tea.KeyPressMsg{}, 9, "↑/↓ select (k/j) · enter details · n new monitor · e edit · d delete · a animation · q quit · ctrl+c quit"},
		{"detail", keyEnter, 21, "esc back · n new monitor · e edit · d delete · a animation · q quit · ctrl+c quit"},
		{"form", keyN, 11, "tab/shift+tab focus · enter save · esc cancel · ctrl+c quit"},
		{"delete", keyD, 7, "y delete · n cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := m
			if tc.name != "list" {
				current, _ = press(t, current, tc.key)
			}
			out := view(current)
			lines := strings.Split(out, "\n")
			if lines[0] != "▲ up  58.5% uptime today · 2 incidents" {
				t.Errorf("header: %q", lines[0])
			}
			if lines[1] != "└"+strings.Repeat("─", 59) {
				t.Errorf("rule: %q", lines[1])
			}
			if len(lines) != tc.lines {
				t.Errorf("lines = %d, want %d:\n%s", len(lines), tc.lines, out)
			}
			if !strings.Contains(out, tc.help) || !strings.HasSuffix(out, "database: /shared/up.db") {
				t.Errorf("lost help/footer:\n%s", out)
			}
		})
	}
}

func TestIdentitySummaryAndWidth(t *testing.T) {
	snap := seededSnapshot(t, now)
	for _, count := range []int{0, 1, 3} {
		current := snap
		current.Services = current.Services[:count]
		m := modelWith(current)
		if strings.Contains(strings.Split(view(m), "\n")[0], "monitor") {
			t.Fatal(view(m))
		}
		for _, tc := range []struct{ width, want int }{{0, 60}, {10, 20}, {42, 42}, {200, 80}} {
			next, _ := m.Update(tea.WindowSizeMsg{Width: tc.width, Height: 24})
			rule := strings.Split(view(next.(Model)), "\n")[1]
			if rule != "└"+strings.Repeat("─", tc.want-1) {
				t.Errorf("width %d: %q", tc.width, rule)
			}
		}
	}
}

func TestIdentityColumns(t *testing.T) {
	snap := seededSnapshot(t, now)
	// Wide monitor names must be padded by terminal cells, not bytes or runes.
	snap.Services[0].Check.Name = "界-api"
	out := view(modelWith(snap))
	header := lineWith(out, "MONITOR")
	column := ansi.StringWidth(strings.Split(header, "STATUS")[0])
	for i, s := range snap.Services {
		row := lineWith(out, s.Check.Name)
		if !strings.Contains(row, "● "+s.Check.Name) {
			t.Errorf("missing dot: %q", row)
		}
		if (i == 0) != strings.HasPrefix(row, "▌ ") {
			t.Errorf("selection: %q", row)
		}
		offset := ansi.StringWidth(strings.Split(row, s.Label())[0])
		if offset != column {
			t.Errorf("status column = %d, header = %d: %q", offset, column, row)
		}
	}
	row := lineWith(out, "payments")
	if !strings.HasSuffix(row, reason) {
		t.Errorf("reason moved: %q", row)
	}
}

func TestIdentityPlainView(t *testing.T) {
	m := modelWith(seededSnapshot(t, now))
	for _, tc := range []struct{ name, noColor, term string }{
		{"no color", "1", "xterm-256color"},
		{"arbitrary nonempty no color", "please", "xterm-256color"},
		{"dumb terminal", "", "dumb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("TERM", tc.term)
			out := m.View().Content
			if out != ansi.Strip(out) {
				t.Fatal("View contains ANSI in plain mode")
			}
			for _, text := range []string{"▲ up", "└─", "▌ ● api", "● payments", "PENDING"} {
				if !strings.Contains(out, text) {
					t.Errorf("missing %q: %s", text, out)
				}
			}
		})
	}
}

func TestIdentityPlainOutput(t *testing.T) {
	m := modelWith(seededSnapshot(t, now))
	for _, env := range [][]string{{"TERM=xterm-256color", "NO_COLOR=1"}, {"TERM=dumb"}} {
		var b bytes.Buffer
		w := colorprofile.NewWriter(&b, env)
		if _, err := w.Write([]byte(m.View().Content)); err != nil {
			t.Fatal(err)
		}
		out := b.String()
		if out != ansi.Strip(out) {
			t.Errorf("escape sequences remain: %q", out)
		}
		for _, text := range []string{"▲ up", "└─", "▌ ● api", "● payments", "PENDING"} {
			if !strings.Contains(out, text) {
				t.Errorf("missing %q: %s", text, out)
			}
		}
	}
}
