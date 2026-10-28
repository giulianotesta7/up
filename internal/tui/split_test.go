package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

func sized(m Model, w, h int) Model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

func TestSplitView(t *testing.T) {
	m := sized(modelWith(seededSnapshot(t, now)), 120, 35)
	out := view(m)
	for _, s := range []string{"api", "payments", "STATUS", "Status      UP", "ACTIVITY", "↑/↓ select · n new"} {
		if !strings.Contains(out, s) {
			t.Errorf("missing %q in view:\n%s", s, out)
		}
	}
	if strings.Contains(out, "enter open") || strings.Contains(out, "esc back") {
		t.Error("pane help leaked into split view")
	}
	for _, line := range strings.Split(m.View().Content, "\n") {
		if ansi.StringWidth(line) > 120 {
			t.Errorf("overwide line: %q", line)
		}
	}
	m, _ = press(t, m, keyEnter, keyDown)
	if m.detail || !strings.Contains(view(m), "Status      DOWN") {
		t.Errorf("selection did not change detail: %s", view(m))
	}
	for _, size := range [][2]int{{100, 35}, {120, 20}} {
		narrow := view(sized(modelWith(seededSnapshot(t, now)), size[0], size[1]))
		if strings.Contains(narrow, "ACTIVITY") || !strings.Contains(narrow, "enter open") {
			t.Errorf("fallback %v: %s", size, narrow)
		}
	}
}

func TestSplitPinnedResize(t *testing.T) {
	snap := seededSnapshot(t, now)
	m := modelWith(snap)
	for _, size := range [][2]int{{110, 30}, {120, 35}, {140, 50}} {
		m = sized(m, size[0], size[1])
		first := strings.Split(view(m), "\n")
		activityLine := -1
		for i, line := range first {
			if strings.Contains(line, "ACTIVITY") {
				activityLine = i
			}
			if ansi.StringWidth(line) > size[0] {
				t.Errorf("%v line %d too wide", size, i)
			}
		}
		if len(first) != size[1] || activityLine < 0 || ansi.StringWidth(first[1]) != size[0] || !strings.Contains(strings.Join(first, "\n"), "LAST CHECK") {
			t.Errorf("%v malformed split (%d lines, activity %d):\n%s", size, len(first), activityLine, strings.Join(first, "\n"))
		}
		if !strings.Contains(strings.Join(first, "\n"), "older") || !strings.Contains(strings.Join(first, "\n"), "now") {
			t.Errorf("%v chart axis labels missing", size)
		}
		m, _ = press(t, m, keyDown)
		second := strings.Split(view(m), "\n")
		if activityLine < 0 || activityLine >= len(second) || !strings.Contains(second[activityLine], "ACTIVITY") {
			t.Errorf("%v activity moved after selection", size)
		}
		m, _ = press(t, m, keyUp)
	}
	m = sized(m, 100, 35)
	if strings.Contains(view(m), "ACTIVITY") || !strings.Contains(view(m), "enter open") {
		t.Error("resize did not restore list")
	}
	m, _ = press(t, m, keyEnter)
	if got, want := m.View().Content, renderTitleWidth(snap, 100)+"\n"+renderHostRule(snap, "", now, 100, -1)+"\n"+renderDetail(snap, snap.Services[0], now, nil, 100)+"\n"+helpStyle.Render("database: "); got != want {
		t.Error("unsplit detail changed")
	}
}

func TestSplitDetailSectionsByHeight(t *testing.T) {
	snap := seededSnapshot(t, now)
	snap.Services[0].HasCert = true
	snap.Services[0].Cert = config.Cert{NotAfter: now.Add(90 * day), Observed: now}
	m := modelWith(snap)
	wide := view(sized(m, 120, 50))
	for _, section := range []string{"CONFIG", "CERTIFICATE", "RECENT OUTAGES"} {
		if !strings.Contains(wide, section) {
			t.Errorf("tall view missing %s", section)
		}
	}
	short := view(sized(m, 120, 30))
	if strings.Contains(short, "CONFIG") {
		t.Error("short view retained CONFIG")
	}
	if !strings.Contains(short, "LATENCY") || !strings.Contains(short, "older") || !strings.Contains(short, "now") {
		t.Errorf("short view lost chart labels:\n%s", short)
	}
}

func TestSplitLongNameFallsBack(t *testing.T) {
	snap := seededSnapshot(t, now)
	snap.Services[0].Check.Name = strings.Repeat("x", 80)
	m := sized(modelWith(snap), 110, 35)
	if strings.Contains(view(m), "ACTIVITY") || !strings.Contains(view(m), "enter open") {
		t.Error("oversized table should fall back to list")
	}
	m = sized(m, 200, 35)
	if !strings.Contains(view(m), "ACTIVITY") || !strings.Contains(view(m), "LAST CHECK") {
		t.Error("wider terminal should fit both panes")
	}
}

func TestSplitActivity(t *testing.T) {
	tests := []struct {
		name         string
		services     []Service
		want, absent []string
	}{
		{name: "outage ordering and ongoing", services: []Service{{Check: config.Check{Name: "api"}, Outages: []store.Outage{{Start: now.Add(-4 * time.Minute), End: now.Add(-3 * time.Minute), Error: "failure"}, {Start: now.Add(-time.Minute), Ongoing: true, Error: "again"}}}}, want: []string{"03:44  ● api went DOWN again", "03:42  ● api recovered after 1m0s", "03:41  ● api went DOWN failure"}, absent: []string{"recovered after -"}},
		{name: "certificate threshold", services: []Service{{Check: config.Check{Name: "near"}, HasCert: true, Cert: config.Cert{Observed: now.Add(-time.Minute), NotAfter: now.Add(29 * day)}}, {Check: config.Check{Name: "boundary"}, HasCert: true, Cert: config.Cert{Observed: now, NotAfter: now.Add(30 * day)}}}, want: []string{"03:44  ! near certificate expires in 29d"}, absent: []string{"! boundary"}},
		{name: "cap five", services: []Service{{Check: config.Check{Name: "api"}, Outages: []store.Outage{{Start: now.Add(-6 * time.Minute), End: now.Add(-5 * time.Minute)}, {Start: now.Add(-4 * time.Minute), End: now.Add(-3 * time.Minute)}, {Start: now.Add(-2 * time.Minute), End: now.Add(-time.Minute)}}}}, want: []string{"03:44", "03:43", "03:42", "03:41", "03:40"}, absent: []string{"03:39"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lines []string
			for _, event := range activity(Snapshot{Services: tt.services}, now) {
				glyph := "●"
				if event.kind == "cert" {
					glyph = "!"
				}
				lines = append(lines, event.at.Format("15:04")+"  "+glyph+" "+event.monitor+" "+event.text)
			}
			out := strings.Join(lines, "\n")
			last := -1
			for _, want := range tt.want {
				pos := strings.Index(out, want)
				if pos < 0 || pos <= last {
					t.Errorf("missing or unordered %q: %s", want, out)
				}
				last = pos
			}
			for _, absent := range tt.absent {
				if strings.Contains(out, absent) {
					t.Errorf("unexpected %q: %s", absent, out)
				}
			}
		})
	}
}
