package tui

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/giulianotesta7/up/internal/check"
)

func TestUptimeTodayLocalMidnightDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []struct {
		month time.Month
		day   int
	}{{time.March, 8}, {time.November, 1}} {
		t.Run(date.month.String(), func(t *testing.T) {
			present := time.Date(2026, date.month, date.day, 4, 0, 0, 0, loc)
			midnight := time.Date(2026, date.month, date.day, 0, 0, 0, 0, loc)
			s, _ := formStore(t)
			ctx := context.Background()
			if err := s.SetChecks(ctx, testChecks[:1]); err != nil {
				t.Fatal(err)
			}
			for _, r := range []check.Result{
				{CheckName: "api", At: midnight.Add(-2 * time.Hour), Status: check.StatusDown},
				{CheckName: "api", At: midnight.Add(-time.Hour), Status: check.StatusUp},
				{CheckName: "api", At: midnight, Status: check.StatusDown},
				{CheckName: "api", At: midnight.Add(time.Hour), Status: check.StatusUp},
			} {
				if err := s.Record(ctx, r); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Beat(ctx, present); err != nil {
				t.Fatal(err)
			}
			snap, err := Load(ctx, s, present)
			want := 100 * float64(present.Sub(midnight)-time.Hour) / float64(present.Sub(midnight))
			if err != nil || !snap.HasUptime || math.Abs(snap.Uptime-want) > 1e-9 || snap.Incidents != 1 {
				t.Fatalf("snapshot %+v err %v, want uptime %v and 1 incident", snap, err, want)
			}
			if line := strings.Split(view(modelWith(snap)), "\n")[0]; !strings.Contains(line, "uptime today") {
				t.Fatal(line)
			}
		})
	}
}

func TestUptimeAggregation(t *testing.T) {
	s, _ := formStore(t)
	ctx := context.Background()
	if err := s.SetChecks(ctx, testChecks); err != nil {
		t.Fatal(err)
	}
	for _, r := range []check.Result{
		{CheckName: "api", At: now.Add(-24 * time.Hour), Status: check.StatusUp},
		{CheckName: "payments", At: now.Add(-time.Hour), Status: check.StatusDown},
		{CheckName: "payments", At: now.Add(-30 * time.Minute), Status: check.StatusUp},
	} {
		if err := s.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Beat(ctx, now); err != nil {
		t.Fatal(err)
	}
	snap, err := Load(ctx, s, now)
	if err != nil || !snap.HasUptime || snap.Uptime != 75 || snap.Incidents != 1 {
		t.Fatalf("%+v %v", snap, err)
	}
	// Stale ends at the heartbeat, not at the injected present time.
	snap, err = Load(ctx, s, now.Add(time.Hour))
	if err != nil || snap.Uptime != 75 || snap.Incidents != 1 {
		t.Fatalf("stale: %+v %v", snap, err)
	}
	emptyStore, _ := formStore(t)
	empty, err := Load(ctx, emptyStore, now)
	if err != nil || empty.HasUptime || empty.Incidents != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
}

func TestUptimeWithoutHeartbeat(t *testing.T) {
	s, _ := formStore(t)
	ctx := context.Background()
	if err := s.SetChecks(ctx, testChecks); err != nil {
		t.Fatal(err)
	}
	for _, r := range []check.Result{
		{CheckName: "api", At: now.Add(-time.Hour), Status: check.StatusUp},
		{CheckName: "payments", At: now.Add(-30 * time.Minute), Status: check.StatusDown},
	} {
		if err := s.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := Load(ctx, s, now)
	if err != nil || snap.HasBeat || snap.HasUptime || snap.Uptime != 0 || snap.Incidents != 1 || !snap.Stale(now) {
		t.Fatalf("snapshot = %+v, err = %v", snap, err)
	}
	line := strings.Split(view(modelWith(snap)), "\n")[0]
	if line != "▲ up" {
		t.Fatalf("header = %q", line)
	}
	if len(snap.Services) != 3 || snap.Services[0].Label() != "UP" || snap.Services[1].Label() != "DOWN" || snap.Services[2].Label() != "PENDING" {
		t.Fatalf("services = %+v", snap.Services)
	}
}

func TestUptimeStaleWindowEndsBeforeToday(t *testing.T) {
	s, _ := formStore(t)
	ctx := context.Background()
	if err := s.SetChecks(ctx, testChecks); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, check.Result{CheckName: "api", At: now.Add(-48 * time.Hour), Status: check.StatusUp}); err != nil {
		t.Fatal(err)
	}
	if err := s.Beat(ctx, now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	snap, err := Load(ctx, s, now)
	if err != nil || snap.HasUptime || snap.Incidents != 0 {
		t.Fatalf("stale heartbeat outside window: %+v %v", snap, err)
	}
}

func TestUptimeHeader(t *testing.T) {
	for _, tc := range []struct {
		percent float64
		text    string
		style   func(...string) string
	}{
		{100, "100%", upStyle.Render}, {99.95, "99.9%", upStyle.Render}, {99.9, "99.9%", upStyle.Render},
		{99.899, "99.8%", slowStyle.Render}, {99, "99.0%", slowStyle.Render}, {98.999, "98.9%", downStyle.Render},
	} {
		snap := Snapshot{HasUptime: true, Uptime: tc.percent, HasBeat: true, LastBeat: now}
		m := modelWith(snap)
		line := strings.Split(m.View().Content, "\n")[0]
		if ansi.Strip(line) != "▲ up  "+tc.text+" uptime today" || !strings.Contains(line, tc.style(tc.text+" uptime today")) {
			t.Fatalf("%v: %q", tc.percent, line)
		}
	}
	for _, n := range []int{0, 1, 2} {
		snap := Snapshot{HasUptime: true, Uptime: 98.7, Incidents: n, HasBeat: true, LastBeat: now}
		m := modelWith(snap)
		line := strings.Split(view(m), "\n")[0]
		want := "▲ up  98.7% uptime today"
		if n == 1 {
			want += " · 1 incident"
		}
		if n == 2 {
			want += " · 2 incidents"
		}
		if line != want {
			t.Fatalf("%q != %q", line, want)
		}
		m.width = 30
		if got := strings.Split(view(m), "\n")[0]; got != "▲ up  98.7% uptime today" {
			t.Fatal(got)
		}
	}
	if line := strings.Split(view(modelWith(Snapshot{})), "\n")[0]; line != "▲ up" {
		t.Fatal(line)
	}
	if sweepFrameInterval != 40*time.Millisecond {
		t.Fatal(sweepFrameInterval)
	}
}

func TestHostnameRule(t *testing.T) {
	snap := Snapshot{HasBeat: true, LastBeat: now, HasUptime: true, Uptime: 100}
	for _, tc := range []struct {
		host   string
		width  int
		prefix string
	}{
		{"server", 60, "└─ server "}, {"", 60, "└───"}, {strings.Repeat("界", 20), 60, "└─ " + strings.Repeat("界", 11) + "… "},
		{strings.Repeat("x", 40), 30, "└───"},
	} {
		m := New(nil, func() time.Time { return now }, Options{Hostname: tc.host})
		m.snap = snap
		m.width = tc.width
		rule := strings.Split(view(m), "\n")[1]
		if !strings.HasPrefix(rule, tc.prefix) || ansi.StringWidth(rule) != tc.width {
			t.Fatalf("%q", rule)
		}
	}
	m := New(nil, func() time.Time { return now }, Options{Hostname: "server"})
	m.snap = snap
	m.snap.LastBeat = now.Add(-2 * time.Minute)
	m.sweeping = true
	m.sweepPosition = 4
	for _, width := range []int{60, 30, 20} {
		m.width = width
		out := view(m)
		rule := strings.Split(out, "\n")[1]
		if !strings.HasPrefix(rule, "└─ server ") || ansi.StringWidth(rule) != width || strings.ContainsAny(rule, "╱╲⚠") {
			t.Fatalf("stale rule width %d: %q", width, rule)
		}
		warning := "daemon not responding: last heartbeat 2m ago — is `up serve` running?"
		if strings.Count(out, warning) != 1 || strings.Contains(out, "daemon stopped") {
			t.Fatalf("stale warning: %q", out)
		}
	}
}

func TestSweepAcrossHostname(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	m := New(nil, func() time.Time { return now }, Options{Hostname: "host"})
	m.snap = Snapshot{HasBeat: true, LastBeat: now}
	m.sweeping = true
	for pos := 0; pos < 59; pos++ {
		m.sweepPosition = pos
		line := strings.Split(m.View().Content, "\n")[1]
		plain := ansi.Strip(line)
		want := []rune("└─ host " + strings.Repeat("─", 52))
		for j, glyph := range []rune("╱╲╱") {
			cell := pos + 1 + j
			if cell < len(want) && want[cell] == '─' {
				want[cell] = glyph
			}
		}
		if ansi.StringWidth(line) != 60 || plain != string(want) {
			t.Fatalf("%d %q, want %q", pos, line, string(want))
		}
		// Host cells 3..6 are lit, never replaced; spaces also remain spaces.
		for i, c := range "host" {
			if 3+i >= pos+1 && 3+i < pos+4 && !strings.Contains(line, accentStyle.Render(string(c))) {
				t.Fatalf("host not lit at %d: %q", pos, line)
			}
		}
		if pos == 2 || pos == 3 || pos == 4 {
			t.Logf("frame %d: %s", pos, plain)
		}
	}
}

func TestHeaderDumps(t *testing.T) {
	for _, tc := range []struct {
		name         string
		pct          float64
		incidents    int
		stale, empty bool
		width        int
	}{
		{"healthy", 100, 0, false, false, 60}, {"incident", 98.7, 1, false, false, 60}, {"stale", 98.7, 1, true, false, 60}, {"no data", 0, 0, false, true, 60}, {"narrow", 99.95, 2, false, false, 30},
	} {
		m := New(nil, func() time.Time { return now }, Options{Hostname: "host"})
		m.width = tc.width
		m.snap = Snapshot{HasBeat: true, LastBeat: now, HasUptime: !tc.empty, Uptime: tc.pct, Incidents: tc.incidents}
		if tc.stale {
			m.snap.LastBeat = now.Add(-2 * time.Minute)
		}
		lines := strings.Split(view(m), "\n")
		t.Logf("%s:\n%s\n%s", tc.name, lines[0], lines[1])
	}
}
