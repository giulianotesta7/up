package client

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

func TestCronHeartbeatThroughTheClient(t *testing.T) {
	c, s, key := realServer(t)
	ctx := context.Background()
	beat := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: 24 * time.Hour, Grace: 30 * time.Minute}
	if err := c.AddCheck(ctx, beat); err != nil {
		t.Fatal(err)
	}
	sealed, _ := s.HeartbeatToken(ctx, "backup")
	token, _ := key.Open(sealed)

	cron := config.Check{Name: "backup", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "0 2 * * 1-5", Grace: 30 * time.Minute}
	if err := c.UpdateCheck(ctx, cron); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Checks(ctx)
	if err != nil || len(stored) != 1 || !reflect.DeepEqual(stored[0], cron) {
		t.Fatalf("stored %+v, %v, want %+v", stored, err, cron)
	}
	if got, err := c.RevealHeartbeatToken(ctx, "backup"); err != nil || got != token {
		t.Fatalf("token after switching to cron = %q, %v, want the old one", got, err)
	}
	bad := cron
	bad.Cron = "0 25 * * *"
	if err := c.UpdateCheck(ctx, bad); err == nil || !strings.Contains(err.Error(), `cron "0 25 * * *" is not valid: hour 25 is out of range 0-23`) {
		t.Fatalf("UpdateCheck(invalid cron) = %v", err)
	}
	if after, _ := s.Checks(ctx); !reflect.DeepEqual(after, stored) {
		t.Fatalf("a rejected update changed %+v", after)
	}
	snap, err := c.LoadSnapshot(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snap.Services[0].Check, cron) {
		t.Fatalf("snapshot check %+v, want %+v", snap.Services[0].Check, cron)
	}
	// Back to period: the wire form of a period heartbeat is unchanged.
	if err := c.UpdateCheck(ctx, beat); err != nil {
		t.Fatal(err)
	}
	if got := checkDTO(beat); got.Schedule != "" || got.Cron != "" {
		t.Fatalf("period wire form %+v carries a schedule", got)
	}
	if stored, _ := s.Checks(ctx); !reflect.DeepEqual(stored[0], beat) {
		t.Fatalf("stored after switching back %+v", stored[0])
	}
}
