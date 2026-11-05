package store_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

func TestDefaultsReadBuiltinWhenNothingStored(t *testing.T) {
	s, _ := seeded(t)
	d, err := s.Defaults(ctx)
	if err != nil || d != config.BuiltinDefaults() {
		t.Fatalf("Defaults() = %+v, %v, want %+v", d, err, config.BuiltinDefaults())
	}
}

func TestSetDefaultsPersistsAcrossReopenAndLeavesExistingItemsAlone(t *testing.T) {
	s, path := seeded(t)
	if err := s.AddSMTPServer(ctx, smtp("s")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, channel("ops")); err != nil {
		t.Fatal(err)
	}
	checksBefore, _ := s.Checks(ctx)
	want := config.Defaults{Zone: "America/Argentina/Buenos_Aires", Channel: "ops", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204, AlertAfter: 3, RecoverAfter: 2}
	if err := s.SetDefaults(ctx, want); err != nil {
		t.Fatal(err)
	}
	// A second save replaces the first (single record).
	want.Zone = "Europe/Madrid"
	if err := s.SetDefaults(ctx, want); err != nil {
		t.Fatal(err)
	}
	s.Close()
	again, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if got, err := again.Defaults(ctx); err != nil || got != want {
		t.Fatalf("Defaults() after reopen = %+v, %v, want %+v", got, err, want)
	}
	if checksAfter, _ := again.Checks(ctx); !reflect.DeepEqual(checksBefore, checksAfter) {
		t.Fatalf("saving defaults changed monitors: %+v -> %+v", checksBefore, checksAfter)
	}
}

func TestSetDefaultsRejectionChangesNothing(t *testing.T) {
	s, path := seeded(t)
	bad := config.BuiltinDefaults()
	bad.Timeout, bad.Interval = 10*time.Second, 5*time.Second
	unchanged(t, path, func() error { return s.SetDefaults(ctx, bad) }, "store: set defaults: timeout (10s) must not exceed interval (5s)")
	bad = config.BuiltinDefaults()
	bad.Zone = "Mars/Base"
	unchanged(t, path, func() error { return s.SetDefaults(ctx, bad) }, `store: set defaults: zone "Mars/Base" is not an IANA time zone`)
	bad = config.BuiltinDefaults()
	bad.Channel = "missing"
	unchanged(t, path, func() error { return s.SetDefaults(ctx, bad) }, `store: set defaults: channel "missing" does not exist`)
	err := s.SetDefaults(ctx, bad)
	if !errors.Is(err, store.ErrValidation) {
		t.Fatalf("error = %v, want ErrValidation", err)
	}
}

func TestDeletingTheDefaultChannelMakesTheDefaultNone(t *testing.T) {
	s, _ := seeded(t)
	if err := s.AddSMTPServer(ctx, smtp("s")); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ops", "other"} {
		if err := s.AddChannel(ctx, channel(n)); err != nil {
			t.Fatal(err)
		}
	}
	d := config.BuiltinDefaults()
	d.Channel = "ops"
	if err := s.SetDefaults(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteChannel(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Defaults(ctx); got.Channel != "ops" {
		t.Fatalf("deleting another channel changed the default: %+v", got)
	}
	if err := s.DeleteChannel(ctx, "ops"); err != nil {
		t.Fatalf("DeleteChannel(default channel) error = %v, want none", err)
	}
	got, err := s.Defaults(ctx)
	d.Channel = ""
	if err != nil || got != d {
		t.Fatalf("Defaults() = %+v, %v, want %+v", got, err, d)
	}
}
