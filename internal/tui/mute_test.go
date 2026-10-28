package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/store"
)

var (
	keyM = tea.KeyPressMsg{Code: 'm', Text: "m"}
	keyU = tea.KeyPressMsg{Code: 'u', Text: "u"}
)

func mutedModel(t *testing.T, statuses ...check.Status) (*store.Store, Model) {
	t.Helper()
	s, m := formStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, testChecks[0]); err != nil {
		t.Fatal(err)
	}
	for i, st := range statuses {
		r := check.Result{CheckName: "api", At: at("03:40:00").Add(time.Duration(i) * 30 * time.Second), Status: st, Latency: time.Millisecond}
		if err := s.Record(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Beat(ctx, now); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	return s, next.(Model)
}

func TestMutePickerMutesAndUnmutes(t *testing.T) {
	s, m := mutedModel(t, check.StatusUp, check.StatusDown)
	m, _ = press(t, m, keyM)
	requireText(t, m, "Mute alerts · api", "Checks keep running. Emails are held until the mute ends.",
		"30 minutes", "1 hour", "4 hours", "24 hours", "Custom…",
		"If api is still down when the mute ends, you get the alert then.", "↑/↓ choose · enter mute · esc cancel")
	m, _ = press(t, m, keyEsc)
	if mute, ok, _ := s.Mute(context.Background(), "api"); ok {
		t.Fatalf("esc stored a mute: %+v", mute)
	}
	m, _ = press(t, m, keyM, keyDown, keyDown)
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	mute, ok, err := s.Mute(context.Background(), "api")
	if err != nil || !ok || !mute.Until.Equal(now.Add(4*time.Hour)) || !mute.WasAlerting {
		t.Fatalf("stored mute %+v %v %v, want 4h with WasAlerting", mute, ok, err)
	}
	list := ansi.Strip(view(m))
	if !strings.Contains(list, "MUTED") || !strings.Contains(list, "⏸ 4h left") {
		t.Fatalf("list lacks the mute:\n%s", list)
	}
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "Muted       until 07:45 (4h left) · nothing pending")

	m, cmd = press(t, m, keyU)
	m = finishCommand(t, m, cmd)
	if mute, _, _ = s.Mute(context.Background(), "api"); !mute.Until.Equal(now) {
		t.Fatalf("unmute until = %v, want now", mute.Until)
	}
	if strings.Contains(ansi.Strip(view(m)), "Muted:") {
		t.Fatalf("detail still shows the mute:\n%s", view(m))
	}
}

func TestMuteCustomDuration(t *testing.T) {
	s, m := mutedModel(t, check.StatusUp)
	m, _ = press(t, m, keyM, keyDown, keyDown, keyDown, keyDown, keyEnter)
	requireText(t, m, "Duration:", "e.g. 45m, 2h, 1h30m")
	m = typeText(t, m, "30s")
	m, cmd := press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("invalid duration started a write")
	}
	requireText(t, m, "Cannot mute: duration must be between 1m and 7d")
	m, _ = press(t, m, keyBack, keyBack, keyBack)
	m = typeText(t, m, "8760h")
	m, _ = press(t, m, keyEnter)
	requireText(t, m, "Cannot mute: duration must be between 1m and 7d")
	m, _ = press(t, m, keyBack, keyBack, keyBack, keyBack, keyBack)
	m = typeText(t, m, "soon")
	m, _ = press(t, m, keyEnter)
	requireText(t, m, `Cannot mute: duration "soon" is not a duration`)
	if _, ok, _ := s.Mute(context.Background(), "api"); ok {
		t.Fatal("rejected input stored a mute")
	}
	m, _ = press(t, m, keyBack, keyBack, keyBack, keyBack)
	m = typeText(t, m, "2h30m")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	mute, ok, _ := s.Mute(context.Background(), "api")
	if !ok || !mute.Until.Equal(now.Add(150*time.Minute)) || mute.WasAlerting {
		t.Fatalf("stored %+v %v", mute, ok)
	}
	requireText(t, m, "⏸ 2h 30m left")
}

func TestDetailShowsHeldEmail(t *testing.T) {
	s, m := mutedModel(t, check.StatusUp, check.StatusDown)
	// The recipients were never told about the failure that began later.
	if err := s.SetMute(context.Background(), "api", now.Add(28*time.Minute), false); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.Init()())
	m, _ = press(t, next.(Model), keyEnter)
	requireText(t, m, "Muted       until 04:13 (28m left) · 1 email pending (down since Oct 25 03:40:30)")
}

func TestMutedColumnIsAlwaysShown(t *testing.T) {
	_, m := mutedModel(t, check.StatusUp)
	list := ansi.Strip(view(m))
	if !strings.Contains(list, "MUTED") || strings.Contains(list, "⏸") {
		t.Fatalf("want a MUTED header and no mute marker:\n%s", list)
	}
}
