package store_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/store"
)

func tagged(name string, tags ...string) config.Check {
	c := definition(name)
	c.Tags = tags
	return c
}

func tagStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "up.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func tagWindow(tag string) config.MaintenanceWindow {
	return config.MaintenanceWindow{Tag: tag, Weekdays: []time.Weekday{time.Monday}, StartMinute: 60, Duration: time.Hour, Zone: "UTC"}
}

func checkTags(t *testing.T, s *store.Store) map[string][]string {
	t.Helper()
	checks, err := s.Checks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, c := range checks {
		out[c.Name] = c.Tags
	}
	return out
}

// Tags are stored normalized in the order typed and come back through every
// write: add, update, copy and a reopen.
func TestTagsRoundTripThroughEveryWrite(t *testing.T) {
	s, path := tagStore(t)
	if err := s.AddCheck(ctx, tagged("api", "NAS", "home", "nas")); err != nil {
		t.Fatal(err)
	}
	beat := config.Check{Name: "db", Kind: config.CheckTCP, Host: "db.local", Port: 5432, Interval: time.Minute, Timeout: time.Second, Tags: []string{"prod"}}
	if err := s.AddCheck(ctx, beat); err != nil {
		t.Fatal(err)
	}
	if got := checkTags(t, s); !reflect.DeepEqual(got, map[string][]string{"api": {"nas", "home"}, "db": {"prod"}}) {
		t.Fatalf("tags after add = %v", got)
	}
	edited := tagged("api", "home", "Prod")
	if err := s.UpdateCheck(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCheckFrom(ctx, "api", tagged("api copy", "home", "prod")); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"api": {"home", "prod"}, "db": {"prod"}, "api copy": {"home", "prod"}}
	if got := checkTags(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("tags after edit and copy = %v", got)
	}
	untagged := definition("api")
	if err := s.UpdateCheck(ctx, untagged); err != nil {
		t.Fatal(err)
	}
	s.Close()
	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if got := checkTags(t, ro); got["api"] != nil || !reflect.DeepEqual(got["api copy"], []string{"home", "prod"}) {
		t.Fatalf("tags after reopen = %v", got)
	}
}

func TestInvalidTagsAreRejectedUnchanged(t *testing.T) {
	s, _ := tagStore(t)
	if err := s.AddCheck(ctx, tagged("api", "nas")); err != nil {
		t.Fatal(err)
	}
	before := checkTags(t, s)
	for _, tags := range [][]string{{"a", "b", "c", "d", "e", "f"}, {"bad tag"}, {strings.Repeat("x", 33)}} {
		if err := s.AddCheck(ctx, tagged("new", tags...)); !errors.Is(err, store.ErrValidation) {
			t.Fatalf("AddCheck(%q) = %v, want ErrValidation", tags, err)
		}
		if err := s.UpdateCheck(ctx, tagged("api", tags...)); !errors.Is(err, store.ErrValidation) {
			t.Fatalf("UpdateCheck(%q) = %v, want ErrValidation", tags, err)
		}
	}
	if got := checkTags(t, s); !reflect.DeepEqual(got, before) {
		t.Fatalf("rejected writes changed tags: %v", got)
	}
}

// A tag's mute covers every monitor carrying the tag when it is read, including
// one tagged after the mute was set. Against the monitor's own mute, the later
// end wins.
func TestTagMuteCoversMembersAndTheLaterEndWins(t *testing.T) {
	s, _ := tagStore(t)
	for _, c := range []config.Check{tagged("api", "nas"), definition("web"), tagged("db", "home")} {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetTagMute(ctx, "ghost", at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetTagMute(unused tag) = %v, want ErrNotFound", err)
	}
	if err := s.SetTagMute(ctx, "bad tag", at); !errors.Is(err, store.ErrValidation) {
		t.Fatalf("SetTagMute(invalid tag) = %v, want ErrValidation", err)
	}
	if err := s.SetTagMute(ctx, "NAS", at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TagMutes(ctx); err != nil || len(got) != 1 || got[0].Tag != "nas" || got[0].Check != "" || !got[0].Until.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("TagMutes = %+v, %v", got, err)
	}
	m, ok, err := s.Mute(ctx, "api")
	if err != nil || !ok || m.Check != "api" || m.Tag != "nas" || !m.Until.Equal(at.Add(2*time.Hour)) {
		t.Fatalf("Mute(api) through the tag = %+v, %v, %v", m, ok, err)
	}
	if _, ok, _ := s.Mute(ctx, "web"); ok {
		t.Fatal("an untagged monitor is muted through a tag")
	}
	// web joins the tag after the mute was set.
	if err := s.UpdateCheck(ctx, tagged("web", "nas")); err != nil {
		t.Fatal(err)
	}
	if m, ok, _ := s.Mute(ctx, "web"); !ok || m.Tag != "nas" {
		t.Fatalf("Mute(web) after joining the tag = %+v, %v", m, ok)
	}
	// An own mute that ends earlier loses; one that ends later wins.
	if err := s.SetMute(ctx, "api", at.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := s.Mute(ctx, "api"); m.Tag != "nas" || !m.Until.Equal(at.Add(2*time.Hour)) || !m.WasAlerting {
		t.Fatalf("earlier own mute = %+v, want the tag's end with the own notification state", m)
	}
	if err := s.SetMute(ctx, "api", at.Add(3*time.Hour), true); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := s.Mute(ctx, "api"); m.Tag != "" || !m.Until.Equal(at.Add(3*time.Hour)) {
		t.Fatalf("later own mute = %+v, want the own one", m)
	}
	mutes, err := s.Mutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range mutes {
		got = append(got, m.Check+"#"+m.Tag+"@"+m.Until.Sub(at).String())
	}
	if want := []string{"api#@3h0m0s", "web#nas@2h0m0s"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Mutes = %v, want %v", got, want)
	}
	// Unmuting the tag ends it for every member at once.
	if err := s.UnmuteTag(ctx, "nas"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Mute(ctx, "web"); ok {
		t.Fatal("web still muted after unmuting its tag")
	}
	if m, ok, _ := s.Mute(ctx, "api"); !ok || m.Tag != "" {
		t.Fatalf("api's own mute after unmuting the tag = %+v, %v", m, ok)
	}
	if err := s.UnmuteTag(ctx, "bad tag"); !errors.Is(err, store.ErrValidation) {
		t.Fatalf("UnmuteTag(invalid) = %v", err)
	}
}

// ClearMute forgets expired tag mutes of the monitor's tags too, so the
// daemon stops finding a mute once everything has ended.
func TestClearMuteForgetsEndedTagMutes(t *testing.T) {
	s, _ := tagStore(t)
	if err := s.AddCheck(ctx, tagged("api", "nas")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTagMute(ctx, "nas", at); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearMute(ctx, "api", at.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Mute(ctx, "api"); !ok {
		t.Fatal("ClearMute removed a tag mute that ends later")
	}
	if err := s.ClearMute(ctx, "api", at); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Mute(ctx, "api"); ok {
		t.Fatal("ended tag mute still found")
	}
}

// A tag's windows apply to every monitor carrying the tag when read, including
// one tagged after the window was created, and are edited through the tag.
func TestTagMaintenanceWindowsCoverMembers(t *testing.T) {
	s, _ := tagStore(t)
	for _, c := range []config.Check{tagged("api", "nas"), definition("web")} {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	own, err := s.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{time.Sunday}, StartMinute: 0, Duration: time.Hour, Zone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMaintenanceWindow(ctx, tagWindow("ghost")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("window for an unused tag = %v, want ErrNotFound", err)
	}
	if _, err := s.AddMaintenanceWindow(ctx, tagWindow("bad tag")); !errors.Is(err, store.ErrValidation) {
		t.Fatalf("window for an invalid tag = %v, want ErrValidation", err)
	}
	w, err := s.AddMaintenanceWindow(ctx, tagWindow("NAS"))
	if err != nil || w.ID == 0 || w.Tag != "nas" {
		t.Fatalf("AddMaintenanceWindow(tag) = %+v, %v", w, err)
	}
	if got, _ := s.MaintenanceWindows(ctx, "web"); len(got) != 0 {
		t.Fatalf("untagged monitor has windows %+v", got)
	}
	if err := s.UpdateCheck(ctx, tagged("web", "nas")); err != nil {
		t.Fatal(err)
	}
	got, err := s.MaintenanceWindows(ctx, "web")
	if err != nil || len(got) != 1 || got[0].ID != w.ID || got[0].Check != "web" || got[0].Tag != "nas" {
		t.Fatalf("MaintenanceWindows(web) after joining = %+v, %v", got, err)
	}
	got, _ = s.MaintenanceWindows(ctx, "api")
	if len(got) != 2 || got[0].ID != own.ID || got[0].Tag != "" || got[1].ID != w.ID || got[1].Tag != "nas" {
		t.Fatalf("MaintenanceWindows(api) = %+v", got)
	}
	all, err := s.AllMaintenanceWindows(ctx)
	var keys []string
	for _, x := range all {
		keys = append(keys, x.Check+"#"+x.Tag)
	}
	if err != nil || !reflect.DeepEqual(keys, []string{"api#", "api#nas", "web#nas"}) {
		t.Fatalf("AllMaintenanceWindows = %v, %v", keys, err)
	}
	if raw, err := s.TagMaintenanceWindows(ctx, "nas"); err != nil || len(raw) != 1 || raw[0].Check != "" || raw[0].Tag != "nas" {
		t.Fatalf("TagMaintenanceWindows = %+v, %v", raw, err)
	}
	// Edits go through the tag; the window cannot move to another tag.
	edit := w
	edit.StartMinute = 120
	if err := s.UpdateMaintenanceWindow(ctx, edit); err != nil {
		t.Fatal(err)
	}
	if raw, _ := s.TagMaintenanceWindows(ctx, "nas"); raw[0].StartMinute != 120 {
		t.Fatalf("after update = %+v", raw)
	}
	edit.Tag = "home"
	if err := s.UpdateMaintenanceWindow(ctx, edit); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update under another tag = %v, want ErrNotFound", err)
	}
	if err := s.DeleteTagMaintenanceWindow(ctx, "home", w.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete under another tag = %v, want ErrNotFound", err)
	}
	// A member can delete the tag window it shows, as it can its own.
	if err := s.DeleteMaintenanceWindow(ctx, "web", w.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MaintenanceWindows(ctx, "api"); len(got) != 1 || got[0].ID != own.ID {
		t.Fatalf("after deleting the tag window = %+v", got)
	}
}

func TestTagWindowsPerTagLimit(t *testing.T) {
	s, _ := tagStore(t)
	if err := s.AddCheck(ctx, tagged("api", "nas")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < store.MaxMaintenanceWindows; i++ {
		if _, err := s.AddMaintenanceWindow(ctx, tagWindow("nas")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddMaintenanceWindow(ctx, tagWindow("nas")); !errors.Is(err, store.ErrValidation) {
		t.Fatalf("window over the limit = %v", err)
	}
	// The monitor's own limit is separate.
	if _, err := s.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{1}, Duration: time.Hour, Zone: "UTC"}); err != nil {
		t.Fatal(err)
	}
}

func seedTagHolds(t *testing.T, s *store.Store) {
	t.Helper()
	for _, c := range []config.Check{tagged("api", "nas", "home"), tagged("web", "nas")} {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, tag := range []string{"nas", "home"} {
		if err := s.SetTagMute(ctx, tag, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddMaintenanceWindow(ctx, tagWindow(tag)); err != nil {
			t.Fatal(err)
		}
	}
}

func tagHolds(t *testing.T, s *store.Store) []string {
	t.Helper()
	mutes, err := s.TagMutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range mutes {
		out = append(out, "mute:"+m.Tag)
	}
	for _, tag := range []string{"home", "nas"} {
		ws, err := s.TagMaintenanceWindows(ctx, tag)
		if err != nil {
			t.Fatal(err)
		}
		for range ws {
			out = append(out, "window:"+tag)
		}
	}
	return out
}

// Removing a tag from its last monitor by an edit deletes that tag's mutes and
// windows; a tag still in use keeps them.
func TestEditRemovingTheLastUseOfATagDeletesItsHolds(t *testing.T) {
	s, _ := tagStore(t)
	seedTagHolds(t, s)
	if err := s.UpdateCheck(ctx, tagged("web")); err != nil {
		t.Fatal(err)
	}
	if got, want := tagHolds(t, s), []string{"mute:home", "mute:nas", "window:home", "window:nas"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("holds while still in use = %v, want %v", got, want)
	}
	if err := s.UpdateCheck(ctx, tagged("api", "home")); err != nil {
		t.Fatal(err)
	}
	if got, want := tagHolds(t, s), []string{"mute:home", "window:home"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("holds after the last nas left = %v, want %v", got, want)
	}
	// Re-adding the tag later starts clean.
	if err := s.UpdateCheck(ctx, tagged("web", "nas")); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Mute(ctx, "web"); ok {
		t.Fatal("a re-added tag brought back an old mute")
	}
}

// Deleting the last monitor with a tag deletes that tag's mutes and windows.
func TestDeleteRemovingTheLastUseOfATagDeletesItsHolds(t *testing.T) {
	s, _ := tagStore(t)
	seedTagHolds(t, s)
	if err := s.DeleteCheck(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if got, want := tagHolds(t, s), []string{"mute:home", "mute:nas", "window:home", "window:nas"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("holds while still in use = %v, want %v", got, want)
	}
	if err := s.DeleteCheck(ctx, "api"); err != nil {
		t.Fatal(err)
	}
	if got := tagHolds(t, s); got != nil {
		t.Fatalf("holds after the last monitor was deleted = %v", got)
	}
	if all, err := s.AllMaintenanceWindows(ctx); err != nil || len(all) != 0 {
		t.Fatalf("windows left = %+v, %v", all, err)
	}
}

// SetChecks, which replaces every monitor, leaves no orphaned tag holds either.
func TestSetChecksRemovingATagDeletesItsHolds(t *testing.T) {
	s, _ := tagStore(t)
	seedTagHolds(t, s)
	if err := s.SetChecks(ctx, []config.Check{tagged("api", "home")}); err != nil {
		t.Fatal(err)
	}
	if got, want := tagHolds(t, s), []string{"mute:home", "window:home"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("holds after SetChecks = %v, want %v", got, want)
	}
}

func TestTagCleanupKeepsOwnWindows(t *testing.T) {
	s, _ := tagStore(t)
	if err := s.AddCheck(ctx, tagged("api", "nas")); err != nil {
		t.Fatal(err)
	}
	own := tagWindow("")
	own.Check = "api"
	if _, err := s.AddMaintenanceWindow(ctx, own); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMaintenanceWindow(ctx, tagWindow("nas")); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateCheck(ctx, tagged("api")); err != nil {
		t.Fatal(err)
	}
	ws, err := s.MaintenanceWindows(ctx, "api")
	if err != nil || len(ws) != 1 || ws[0].Tag != "" {
		t.Fatalf("windows = %+v, %v; want only the own window", ws, err)
	}
}
