package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/store"
)

func TestTrendViews(t *testing.T) {
	snap := seededSnapshot(t, now)
	snap.Services[0].Check.Name = "界-api"
	for _, width := range []int{0, 77, 78, 120} {
		m := modelWith(snap)
		next, _ := m.Update(tea.WindowSizeMsg{Width: width})
		out := view(next.(Model))
		header := lineWith(out, "MONITOR")
		show := width == 0 || width >= 78
		if strings.Contains(header, "TREND") != show {
			t.Errorf("width %d: %s", width, out)
		}
		if !show {
			continue
		}
		trend := ansi.StringWidth(strings.Split(header, "TREND")[0])
		last := ansi.StringWidth(strings.Split(header, "LAST CHECK")[0])
		for _, s := range snap.Services {
			row := lineWith(out, s.Check.Name)
			cells := []rune(row)
			// Convert the terminal-cell offset for the wide name to rune offsets.
			offset := 0
			if strings.Contains(row, "界") {
				offset = 1
			}
			if len(cells) < last-offset {
				t.Fatalf("short row %q", row)
			}
			got := string(cells[trend-offset : trend-offset+16])
			if got != ansi.Strip(sparkline(s.Recent, 16)) {
				t.Errorf("trend misaligned: %q", row)
			}
			if s.HasResult && !strings.HasPrefix(string(cells[last-offset:]), ago(now, s.Latest.At)) {
				t.Errorf("last check misaligned: %q", row)
			}
		}
	}
}

func TestGraphDetailViews(t *testing.T) {
	snap := seededSnapshot(t, now)
	for _, tc := range []struct {
		input []int
		want  string
	}{
		{[]int{420, 100, 100, 100, 100, -1, -1}, "avg 164ms · p95 420ms · max 420ms · 2 failed of 7"},
		{[]int{-1, -1}, "no successful checks · 2 failed of 2"},
	} {
		snap.Services[0].Recent = samples(tc.input...)
		for _, width := range []int{0, 25, 80} {
			m := modelWith(snap)
			m.detail = true
			next, _ := m.Update(tea.WindowSizeMsg{Width: width})
			out := view(next.(Model))
			if !strings.Contains(out, tc.want) || !strings.Contains(out, "older") || !strings.Contains(out, "now") || !strings.Contains(out, "0│") || !strings.Contains(out, "░") {
				t.Fatal(out)
			}
			baseline := ""
			for _, line := range strings.Split(out, "\n") {
				if strings.Contains(line, "└─") && strings.HasPrefix(line, " ") {
					baseline = line
				}
			}
			wantWidth := 28
			if width == 25 {
				wantWidth = 14
			}
			if strings.Count(baseline, "─") != wantWidth {
				t.Errorf("width %d: %q", width, baseline)
			}
		}
	}
}

func TestGraphPlainViews(t *testing.T) {
	for _, env := range []struct{ noColor, term string }{{"1", "xterm"}, {"", "dumb"}} {
		t.Setenv("NO_COLOR", env.noColor)
		t.Setenv("TERM", env.term)
		m := modelWith(seededSnapshot(t, now))
		if out := m.View().Content; out != ansi.Strip(out) || !strings.Contains(out, "✕") {
			t.Fatal(out)
		}
		m.selected = 1
		m.detail = true
		if out := m.View().Content; out != ansi.Strip(out) || !strings.Contains(out, "░") || !strings.Contains(out, "█") {
			t.Fatal(out)
		}
	}
}

func TestDetailStatsUseVisibleWindow(t *testing.T) {
	snap := seededSnapshot(t, now)
	snap.Services[0].Recent = samples(10, 10, 10, 10, 10, 10, 10, 10, 1000, -1)
	m := modelWith(snap)
	m.detail = true
	next, _ := m.Update(tea.WindowSizeMsg{Width: 19})
	out := view(next.(Model))
	if !strings.Contains(out, "avg 10ms · p95 10ms · max 10ms · 0 failed of 8") || strings.Contains(out, "1s│") {
		t.Fatal(out)
	}
}

func TestLoadGraphWindow(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.SetChecks(ctx, testChecks[:1]); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		r := up("api", "03:44:30")
		r.At = r.At.Add(time.Duration(i) * time.Second)
		if err := s.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := Load(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	svc := snap.Services[0]
	if len(svc.Recent) != 28 || svc.Latest != svc.Recent[0] || !svc.HasResult || !svc.Recent[27].At.Equal(at("03:44:32")) {
		t.Fatalf("window: %+v", svc)
	}
}
