package client

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/tui"
)

var _ tui.HeartbeatTokens = (*Client)(nil)

func TestHeartbeatThroughTheClient(t *testing.T) {
	c, s, key := realServer(t)
	ctx := context.Background()
	beat := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: 24 * time.Hour, Grace: 30 * time.Minute, AlertAfter: 2}
	if err := c.AddCheck(ctx, beat); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Checks(ctx)
	if err != nil || len(stored) != 1 || !reflect.DeepEqual(stored[0], beat) {
		t.Fatalf("stored %+v, %v, want %+v", stored, err, beat)
	}

	token, err := c.RevealHeartbeatToken(ctx, "backup")
	sealed, _ := s.HeartbeatToken(ctx, "backup")
	if want, _ := key.Open(sealed); err != nil || token != want {
		t.Fatalf("RevealHeartbeatToken() = %q, %v, want %q", token, err, want)
	}
	fresh, err := c.RegenerateHeartbeatToken(ctx, "backup")
	if err != nil || fresh == token || !strings.HasPrefix(fresh, "uphb_") {
		t.Fatalf("RegenerateHeartbeatToken() = %q, %v", fresh, err)
	}
	if got, err := c.RevealHeartbeatToken(ctx, "backup"); err != nil || got != fresh {
		t.Fatalf("RevealHeartbeatToken() after regenerate = %q, %v", got, err)
	}
	if found, _ := s.PingHeartbeat(ctx, token, false, time.Now()); found {
		t.Fatal("the old token still works")
	}
	if _, err := c.RevealHeartbeatToken(ctx, "missing"); err == nil || !strings.Contains(err.Error(), `heartbeat monitor "missing" not found`) {
		t.Fatalf("RevealHeartbeatToken(missing) = %v", err)
	}
	if _, err := c.RegenerateHeartbeatToken(ctx, ".."); err == nil {
		t.Fatal("an unaddressable name was sent")
	}

	pinged := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	if found, err := s.PingHeartbeat(ctx, fresh, false, pinged); err != nil || !found {
		t.Fatal(found, err)
	}
	if err := s.SetAPIListener(ctx, config.APIListener{Address: "[::]:8443", TLS: true}); err != nil {
		t.Fatal(err)
	}
	snap, err := c.LoadSnapshot(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	svc := snap.Services[0]
	if !reflect.DeepEqual(svc.Check, beat) || svc.Label() != "UP" || snap.API != (config.APIListener{Address: "[::]:8443", TLS: true}) {
		t.Fatalf("snapshot check %+v label %q API %+v", svc.Check, svc.Label(), snap.API)
	}
	// Times follow this machine's clock like the rest of the snapshot.
	if d := svc.Heartbeat.LastPing.Sub(pinged); d < -time.Second || d > time.Second || svc.Heartbeat.Deadline.Sub(svc.Heartbeat.LastPing) != beat.Period+beat.Grace || svc.Heartbeat.Waiting {
		t.Fatalf("heartbeat state %+v, pinged %v", svc.Heartbeat, pinged)
	}
	if strings.Contains(fmt.Sprintf("%#v", snap), fresh) {
		t.Fatal("the snapshot carries the token")
	}
}

func TestHeartbeatTokenSealedToAnotherKey(t *testing.T) {
	c, s, _ := realServer(t)
	ctx := context.Background()
	if err := c.AddCheck(ctx, config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: time.Hour}); err != nil {
		t.Fatal(err)
	}
	// The stored public key no longer matches the server's private key (the
	// key file was replaced): new tokens cannot be opened by this server.
	other, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicKey(ctx, other.PublicKey()); err != nil {
		t.Fatal(err)
	}
	fresh, err := c.RegenerateHeartbeatToken(ctx, "backup")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.RevealHeartbeatToken(ctx, "backup")
	if err == nil || err.Error() != "the token was sealed with another encryption key; replace the token" || strings.Contains(err.Error(), fresh) {
		t.Fatalf("RevealHeartbeatToken() with another key = %v", err)
	}
}
