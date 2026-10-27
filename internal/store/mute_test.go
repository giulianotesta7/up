package store

import (
	"context"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

func TestMuteLifecycle(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for _, name := range []string{"z", "a"} {
		if err := s.AddCheck(ctx, config.Check{Name: name, URL: "http://example.com", Interval: time.Second, Timeout: time.Second, ExpectedStatus: 200}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetMute(ctx, "missing", t0, false); err == nil || err.Error() != `store: set mute: name "missing" not found` {
		t.Fatalf("missing: %v", err)
	}
	if rows, err := s.Mutes(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("mutes after rejection: %v %v", rows, err)
	}
	if err := s.SetMute(ctx, "z", t0, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMute(ctx, "z", t0.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMute(ctx, "a", t0, false); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Mutes(ctx)
	if err != nil || len(rows) != 2 || rows[0].Check != "a" || rows[1].Check != "z" || !rows[1].WasAlerting || !rows[1].Until.Equal(t0.Add(time.Hour)) {
		t.Fatalf("mutes: %+v %v", rows, err)
	}
	if err := s.Unmute(ctx, "z", t0); err != nil {
		t.Fatal(err)
	}
	m, ok, err := s.Mute(ctx, "z")
	if err != nil || !ok || !m.Until.Equal(t0) || !m.WasAlerting {
		t.Fatalf("mute: %+v %v %v", m, ok, err)
	}
	if err := s.DeleteCheck(ctx, "z"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Mute(ctx, "z"); err != nil || ok {
		t.Fatalf("deleted mute: %v %v", ok, err)
	}
	if err := s.ClearMute(ctx, "a", t0.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if m, ok, _ := s.Mute(ctx, "a"); !ok || !m.Until.Equal(t0) {
		t.Fatalf("clear removed a mute that ends later: %+v %v", m, ok)
	}
	if err := s.ClearMute(ctx, "a", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.Unmute(ctx, "a", t0); err != nil {
		t.Fatal(err)
	}
	if rows, err := s.Mutes(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("remaining: %v %v", rows, err)
	}
}
