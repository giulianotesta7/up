package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

// heartbeatStore is a local database next to its daemon key, as `up serve`
// leaves it, with a heartbeat "backup" (period 1h, grace 10m).
func heartbeatStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "up.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	key, _, err := secret.LoadOrCreate(path + ".key")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicKey(context.Background(), key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCheck(context.Background(), config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: time.Hour, Grace: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestLoadHeartbeatStates(t *testing.T) {
	s, path := heartbeatStore(t)
	ctx := context.Background()
	snap, err := Load(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	svc := snap.Services[0]
	if svc.Label() != "WAITING" || svc.Heartbeat != (HeartbeatState{Waiting: true}) {
		t.Fatalf("before the first ping: label %q, state %+v", svc.Label(), svc.Heartbeat)
	}
	if snap.API.Enabled() {
		t.Fatalf("API = %+v before any daemon start", snap.API)
	}

	tokens := NewLocalHeartbeatTokens(s, path)
	token, err := tokens.RevealHeartbeatToken(ctx, "backup")
	if err != nil {
		t.Fatal(err)
	}
	ping := now.Add(-30 * time.Minute)
	if found, err := s.PingHeartbeat(ctx, token, false, ping); err != nil || !found {
		t.Fatalf("PingHeartbeat() = %v, %v", found, err)
	}
	api := config.APIListener{Address: "[::]:8080", TLS: true}
	if err := s.SetAPIListener(ctx, api); err != nil {
		t.Fatal(err)
	}
	snap, err = Load(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	svc = snap.Services[0]
	want := HeartbeatState{LastPing: ping, Deadline: ping.Add(70 * time.Minute)}
	if svc.Label() != "UP" || svc.Heartbeat != want || snap.API != api {
		t.Fatalf("after a ping: label %q, state %+v, API %+v; want %+v", svc.Label(), svc.Heartbeat, snap.API, want)
	}

	// A reported failure keeps the last ping and starts a new period.
	failed := now.Add(-5 * time.Minute)
	if found, err := s.PingHeartbeat(ctx, token, true, failed); err != nil || !found {
		t.Fatalf("PingHeartbeat(fail) = %v, %v", found, err)
	}
	snap, err = Load(ctx, s, now)
	if err != nil {
		t.Fatal(err)
	}
	svc = snap.Services[0]
	if want := (HeartbeatState{LastPing: ping, Deadline: failed.Add(70 * time.Minute)}); svc.Label() != "DOWN" || svc.Heartbeat != want || !check.Failed(svc.Latest) {
		t.Fatalf("after /fail: label %q, state %+v, latest %+v; want %+v", svc.Label(), svc.Heartbeat, svc.Latest, want)
	}
	if strings.Contains(fmt.Sprintf("%#v", snap), token) {
		t.Fatal("the snapshot carries the ping token")
	}
}

func TestLocalHeartbeatTokensRevealAndRegenerate(t *testing.T) {
	s, path := heartbeatStore(t)
	ctx := context.Background()
	tokens := NewLocalHeartbeatTokens(s, path)
	old, err := tokens.RevealHeartbeatToken(ctx, "backup")
	if err != nil || !strings.HasPrefix(old, "uphb_") {
		t.Fatalf("RevealHeartbeatToken() = %q, %v", old, err)
	}
	fresh, err := tokens.RegenerateHeartbeatToken(ctx, "backup")
	if err != nil || fresh == old {
		t.Fatalf("RegenerateHeartbeatToken() = %q, %v (old %q)", fresh, err, old)
	}
	if got, err := tokens.RevealHeartbeatToken(ctx, "backup"); err != nil || got != fresh {
		t.Fatalf("RevealHeartbeatToken() after regenerate = %q, %v, want %q", got, err, fresh)
	}
	if found, _ := s.PingHeartbeat(ctx, old, false, now); found {
		t.Fatal("the old token still works")
	}
	if _, err := tokens.RevealHeartbeatToken(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RevealHeartbeatToken(missing) = %v", err)
	}
}

func TestLocalRevealWithoutAReadableKeyExplainsWhy(t *testing.T) {
	s, path := heartbeatStore(t)
	ctx := context.Background()
	// Another database path has no key next to it (the daemon never ran
	// there): nothing is created and the hint says what to do.
	elsewhere := filepath.Join(t.TempDir(), "up.db")
	_, err := NewLocalHeartbeatTokens(s, elsewhere).RevealHeartbeatToken(ctx, "backup")
	if !errors.Is(err, ErrRevealUnavailable) || !strings.Contains(err.Error(), "cannot read the encryption key "+elsewhere+".key") || !strings.Contains(err.Error(), "run up tui as the user that runs up serve, or use up tui --remote") {
		t.Fatalf("reveal without a key = %v", err)
	}
	if _, statErr := os.Lstat(elsewhere + ".key"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("revealing created a key: %v", statErr)
	}
	if os.Geteuid() != 0 {
		// A key the TUI's user may not read (a system service).
		if err := os.Chmod(path+".key", 0); err != nil {
			t.Fatal(err)
		}
		_, err = NewLocalHeartbeatTokens(s, path).RevealHeartbeatToken(ctx, "backup")
		if !errors.Is(err, ErrRevealUnavailable) || !strings.Contains(err.Error(), "cannot read the encryption key "+path+".key") {
			t.Fatalf("reveal with an unreadable key = %v", err)
		}
		if err := os.Chmod(path+".key", 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A token sealed to another key (the key was replaced) cannot be opened.
	other, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicKey(ctx, other.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegenerateHeartbeatToken(ctx, "backup"); err != nil {
		t.Fatal(err)
	}
	_, err = NewLocalHeartbeatTokens(s, path).RevealHeartbeatToken(ctx, "backup")
	if !errors.Is(err, ErrRevealUnavailable) || !strings.Contains(err.Error(), "sealed with another encryption key; replace the token") {
		t.Fatalf("reveal with another key = %v", err)
	}
}
