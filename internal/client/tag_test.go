package client

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/tui"
)

// Tags, tag mutes and tag windows travel client -> API -> store -> snapshot ->
// client.
func TestTagsRoundTripThroughTheRealAPI(t *testing.T) {
	c, s, _ := realServer(t)
	ctx := context.Background()
	api := newCheck("api")
	api.Tags = []string{"NAS", "home"}
	if err := c.AddCheck(ctx, api); err != nil {
		t.Fatal(err)
	}
	if err := c.AddCheckFrom(ctx, "api", func() config.Check { v := newCheck("api copy"); v.Tags = []string{"nas"}; return v }()); err != nil {
		t.Fatal(err)
	}
	load := func() tui.Snapshot {
		t.Helper()
		snap, err := c.LoadSnapshot(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return snap
	}
	snap := load()
	if !reflect.DeepEqual(snap.Services[0].Check.Tags, []string{"nas", "home"}) || !reflect.DeepEqual(snap.Services[1].Check.Tags, []string{"nas"}) {
		t.Fatalf("snapshot tags = %q, %q", snap.Services[0].Check.Tags, snap.Services[1].Check.Tags)
	}
	// An edit keeps whatever tags it sends.
	edited := snap.Services[0].Check
	edited.Tags = []string{"home", "prod"}
	if err := c.UpdateCheck(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if got := load().Services[0].Check.Tags; !reflect.DeepEqual(got, []string{"home", "prod"}) {
		t.Fatalf("tags after edit = %q", got)
	}
	bad := edited
	bad.Tags = []string{"bad tag"}
	if err := c.UpdateCheck(ctx, bad); err == nil || !strings.Contains(err.Error(), `tag "bad tag" may only use letters a-z, digits and -`) {
		t.Fatalf("invalid tag = %v", err)
	}

	// Tag mute: set, seen through the snapshot, then ended.
	if err := c.SetTagMute(ctx, "prod", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc := load().Services[0]
	if svc.Mute.Tag != "prod" || !svc.Muted(time.Now()) {
		t.Fatalf("mute through the tag = %+v", svc.Mute)
	}
	if err := c.SetTagMute(ctx, "ghost", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("muted an unused tag")
	}
	if err := c.UnmuteTag(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if svc := load().Services[0]; svc.HasMute {
		t.Fatalf("still muted: %+v", svc.Mute)
	}

	// Tag window: added, edited and deleted through the tag.
	w, err := c.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Tag: "home", Weekdays: []time.Weekday{time.Monday}, StartMinute: 60, Duration: time.Hour, Zone: "UTC"})
	if err != nil || w.ID == 0 || w.Tag != "home" {
		t.Fatalf("AddMaintenanceWindow(tag) = %+v, %v", w, err)
	}
	if raw, _ := s.TagMaintenanceWindows(ctx, "home"); len(raw) != 1 || raw[0].ID != w.ID {
		t.Fatalf("stored tag windows = %+v", raw)
	}
	svc = load().Services[0]
	if len(svc.Maintenance) != 1 || svc.Maintenance[0].Tag != "home" || svc.Maintenance[0].Check != "api" {
		t.Fatalf("snapshot windows = %+v", svc.Maintenance)
	}
	w.StartMinute = 120
	if err := c.UpdateMaintenanceWindow(ctx, w); err != nil {
		t.Fatal(err)
	}
	if raw, _ := s.TagMaintenanceWindows(ctx, "home"); raw[0].StartMinute != 120 {
		t.Fatalf("after update = %+v", raw)
	}
	if err := c.DeleteTagMaintenanceWindow(ctx, "home", w.ID); err != nil {
		t.Fatal(err)
	}
	if raw, _ := s.TagMaintenanceWindows(ctx, "home"); len(raw) != 0 {
		t.Fatalf("after delete = %+v", raw)
	}
}
