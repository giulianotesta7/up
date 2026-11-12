package tui

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestUOnOwnLaterMuteUnmutesOnlyTheMonitor(t *testing.T) {
	s, m := tagStore(t)
	ctx := context.Background()
	if err := s.SetTagMute(ctx, "nas", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMute(ctx, "api", now.Add(3*time.Hour), false); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	m.width, m.height = 80, 24
	m, cmd := press(t, m, keyU)
	if strings.Contains(view(m), "Unmute #nas") {
		t.Fatalf("asked about the tag:\n%s", view(m))
	}
	finishCommand(t, m, cmd)
	if mutes, _ := s.TagMutes(ctx); len(mutes) != 1 {
		t.Fatalf("tag mute removed: %+v", mutes)
	}
	if mu, ok, _ := s.Mute(ctx, "api"); !ok || mu.Tag != "nas" {
		t.Fatalf("api = %+v %v, want held through #nas", mu, ok)
	}
}

func TestUnmutingTheLaterTagKeepsTheShorterOwnMute(t *testing.T) {
	s, m := tagStore(t)
	ctx := context.Background()
	if err := s.SetTagMute(ctx, "nas", now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMute(ctx, "api", now.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	m, _ = press(t, m, keyU)
	requireText(t, m, "Unmute #nas")
	m, cmd := press(t, m, keyY)
	finishCommand(t, m, cmd)
	if mutes, _ := s.TagMutes(ctx); len(mutes) != 0 {
		t.Fatalf("tag mute left: %+v", mutes)
	}
	if mu, ok, _ := s.Mute(ctx, "api"); !ok || mu.Tag != "" || !mu.Until.Equal(now.Add(time.Hour)) {
		t.Fatalf("own mute = %+v %v", mu, ok)
	}
}
