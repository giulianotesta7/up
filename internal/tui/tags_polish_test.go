package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
)

// heldThroughTag is api muted through #nas with a down alert held.
func heldThroughTag() (Snapshot, Service) {
	snap := tagSnapshot()
	svc := snap.Services[1]
	svc.HasMute = true
	svc.Mute = config.Mute{Check: svc.Check.Name, Until: now.Add(29 * time.Minute), Tag: "nas"}
	svc.Streak = check.Streak{Alerting: true}
	svc.DownSince = now.Add(-33 * time.Minute)
	snap.Services[1] = svc
	return snap, svc
}

func TestMutedLineThroughTagFits80Columns(t *testing.T) {
	snap, svc := heldThroughTag()
	wide := ansi.Strip(renderDetail(snap, svc, now, nil, 160))
	if want := "  Muted        until 04:14 (29m left) · through #nas · 1 alert pending (down since Oct 25 03:12:00)\n"; !strings.Contains(wide, want) {
		t.Fatalf("wide detail lacks %q:\n%s", want, wide)
	}
	narrow := ansi.Strip(renderDetail(snap, svc, now, nil, 80))
	if want := "  Muted        until 04:14 (29m left) · through #nas · 1 alert pending (down)\n"; !strings.Contains(narrow, want) {
		t.Fatalf("80-column detail lacks %q:\n%s", want, narrow)
	}
	// A long tag leaves no room on one line: what is held goes below.
	svc.Mute.Tag = "storage-cluster-west"
	narrow = ansi.Strip(renderDetail(snap, svc, now, nil, 80))
	if want := "  Muted        until 04:14 (29m left) · through #storage-cluster-west\n               1 alert pending (down since Oct 25 03:12:00)\n"; !strings.Contains(narrow, want) {
		t.Fatalf("80-column detail lacks %q:\n%s", want, narrow)
	}
	for _, line := range strings.Split(narrow, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("line wider than 80: %q", line)
		}
	}
}

// The mute popup and the window form say who a tag covers the same way:
// names when they fit, a count otherwise, and the same tail.
func TestTagCoverageWordingMatchesInMuteAndWindow(t *testing.T) {
	covers := func(t *testing.T, m Model) (mute, window string) {
		t.Helper()
		m.width, m.height = 80, 24
		mute = renderAt(t, func() Model { next, _ := press(t, m, keyM, keyRight); return next }(), 80, 24)
		window = renderAt(t, func() Model { next, _ := press(t, m, keyW, keyA, keyRight); return next }(), 80, 24)
		return mute, window
	}
	s, m := tagStore(t)
	mute, window := covers(t, m)
	if want := "covers api, db and any tagged later"; !strings.Contains(mute, "Mute ‹ #nas ›  "+want) || !strings.Contains(window, "‹ #nas ›   "+want) {
		t.Fatalf("want %q in both:\n%s\n%s", want, mute, window)
	}
	for _, name := range []string{"nas-backup-primary", "nas-backup-secondary", "nas-media-library"} {
		if err := s.AddCheck(context.Background(), config.Check{Name: name, Kind: config.CheckTCP, Host: "nas", Port: 445, Interval: time.Minute, Timeout: 2 * time.Second, Tags: []string{"nas"}}); err != nil {
			t.Fatal(err)
		}
	}
	mute, window = covers(t, reload(t, m))
	if want := "covers 5 monitors and any tagged later"; !strings.Contains(mute, "Mute ‹ #nas ›  "+want) || !strings.Contains(window, "‹ #nas ›   "+want) {
		t.Fatalf("want %q in both:\n%s\n%s", want, mute, window)
	}
}

func TestTagsWarningKeepsTheFormWidth(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {160, 50}} {
		_, m := tagStore(t)
		m.width, m.height = size[0], size[1]
		m, _ = press(t, m, keyN)
		m = focusRow(t, m, "Tags")
		left, width := modalBorder(t, m, "New monitor")
		for _, r := range "a, b, c, d, e, f" {
			m = typeText(t, m, string(r))
			if l, w := modalBorder(t, m, "New monitor"); l != left || w != width {
				t.Fatalf("%dx%d: typing %q moved the popup: %d/%d -> %d/%d\n%s", size[0], size[1], m.form.tags, left, width, l, w, view(m))
			}
		}
		requireText(t, m, "6 new tags")
	}
}

func TestUnmuteTagPromptWithAShorterOwnMute(t *testing.T) {
	s, m := tagStore(t)
	ctx := context.Background()
	if err := s.SetTagMute(ctx, "nas", now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMute(ctx, "api", now.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	m.width, m.height = 80, 24
	m, _ = press(t, m, keyU)
	screen := renderAt(t, m, 80, 24)
	for _, want := range []string{"api is muted through #nas.", "Unmuting ends the #nas mute for api and db.", "Mutes set on a single monitor stay."} {
		if !strings.Contains(screen, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "Unmuting ends the mute for") {
		t.Fatalf("prompt says api's own mute ends too:\n%s", screen)
	}
}
