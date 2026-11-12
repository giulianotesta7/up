package store_test

import (
	"testing"
	"time"
)

func TestOwnMuteWinsATie(t *testing.T) {
	s, _ := tagStore(t)
	if err := s.AddCheck(ctx, tagged("api", "nas")); err != nil {
		t.Fatal(err)
	}
	until := at.Add(time.Hour)
	if err := s.SetMute(ctx, "api", until, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTagMute(ctx, "nas", until); err != nil {
		t.Fatal(err)
	}
	if m, ok, err := s.Mute(ctx, "api"); err != nil || !ok || m.Tag != "" || !m.WasAlerting {
		t.Fatalf("tie: Mute(api) = %+v %v %v, want the own mute", m, ok, err)
	}
	if mutes, err := s.Mutes(ctx); err != nil || len(mutes) != 1 || mutes[0].Tag != "" {
		t.Fatalf("tie: Mutes = %+v %v, want the own mute", mutes, err)
	}
}
