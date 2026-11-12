package tui

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
)

// tagStore is a real store with tagged monitors of every kind: api (HTTP,
// #nas #home), db (TCP, #nas), job (heartbeat, #home) and payments (HTTP,
// #prod). The daemon beat at now.
func tagStore(t *testing.T) (*store.Store, Model) {
	t.Helper()
	s, m := formStore(t)
	ctx := context.Background()
	key, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "up.db.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicKey(ctx, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []config.Check{
		{Name: "api", URL: "https://api.example.com/health", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200, Tags: []string{"nas", "home"}},
		{Name: "db", Kind: config.CheckTCP, Host: "10.0.0.5", Port: 5432, Interval: time.Minute, Timeout: 2 * time.Second, Tags: []string{"nas"}},
		{Name: "job", Kind: config.CheckHeartbeat, Period: time.Hour, Grace: 5 * time.Minute, Tags: []string{"home"}},
		{Name: "payments", URL: "https://pay.example.com", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204, Tags: []string{"prod"}},
	} {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Record(ctx, check.Result{CheckName: "api", At: at("03:44:30"), Status: check.StatusUp, StatusCode: 200, Latency: 84 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if err := s.Beat(ctx, now); err != nil {
		t.Fatal(err)
	}
	return s, reload(t, m)
}

// focusRow tabs until the focused row starts with "> label:".
func focusRow(t *testing.T, m Model, label string) Model {
	t.Helper()
	for range 40 {
		if strings.Contains(view(m), "> "+label+":") {
			return m
		}
		m, _ = press(t, m, keyTab)
	}
	t.Fatalf("no focusable %s row:\n%s", label, view(m))
	return m
}

func tagsOf(t *testing.T, s *store.Store, name string) []string {
	t.Helper()
	c, ok := storedByName(t, s, name)
	if !ok {
		t.Fatalf("no monitor %s", name)
	}
	return c.Tags
}

func TestFormTagsRowHintsAndNewTagWarning(t *testing.T) {
	s, m := tagStore(t)
	m.width, m.height = 160, 50
	m, _ = press(t, m, keyN)
	m = typeText(t, m, "nas-web")
	m, _ = press(t, m, keyTab)
	m = typeText(t, m, "https://nas.home.arpa")
	m = focusRow(t, m, "Tags")
	requireText(t, m, "> Tags: │   comma-separated · in use: home, nas, prod")
	m = typeText(t, m, "nas, hom")
	requireText(t, m, "> Tags: nas, hom│   comma-separated · hom is a new tag")
	if strings.Contains(view(m), "in use:") {
		t.Fatalf("the new tag warning replaces the list:\n%s", view(m))
	}
	m, _ = press(t, m, keyBack, keyBack, keyBack)
	m = typeText(t, m, "HOME")
	requireText(t, m, "> Tags: nas, HOME│   comma-separated · in use: home, nas, prod")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	if got := tagsOf(t, s, "nas-web"); !reflect.DeepEqual(got, []string{"nas", "home"}) {
		t.Fatalf("stored tags %q, want [nas home]", got)
	}
}

func TestFormTagsRowOnEveryKindAndRejectedTags(t *testing.T) {
	for row, name := range []string{"api", "db", "job"} {
		t.Run(name, func(t *testing.T) {
			s, m := tagStore(t)
			for range row {
				m, _ = press(t, m, keyDown)
			}
			before := tagsOf(t, s, name)
			m, _ = press(t, m, keyE)
			m = focusRow(t, m, "Tags")
			m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
			m = replaceField(t, m, config.FormatTags(before), "")
			for _, tc := range []struct{ tags, want string }{
				{"x y", `Cannot save monitor: tag "x y" may only use letters a-z, digits and -`},
				{"a, b, c, d, e, f", "Cannot save monitor: at most 5 tags are allowed"},
				{strings.Repeat("a", 33), `Cannot save monitor: tag "` + strings.Repeat("a", 33) + `" must be at most 32 characters`},
			} {
				m = typeText(t, m, tc.tags)
				var cmd tea.Cmd
				m, cmd = press(t, m, keyEnter)
				if cmd != nil {
					t.Fatalf("%q was submitted", tc.tags)
				}
				requireText(t, m, tc.want)
				for range []rune(tc.tags) {
					m, _ = press(t, m, keyBack)
				}
			}
			if got := tagsOf(t, s, name); !reflect.DeepEqual(got, before) {
				t.Fatalf("rejected tags changed the store: %q, want %q", got, before)
			}
		})
	}
}

// Every kind's edit and duplicate carry the tags: an edit that dropped them
// would also delete the tag's mute and windows.
func TestEditAndDuplicateKeepTags(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  int
		tags []string
	}{
		{"api", 0, []string{"nas", "home"}},
		{"db", 1, []string{"nas"}},
		{"job", 2, []string{"home"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := tagStore(t)
			ctx := context.Background()
			until := now.Add(time.Hour)
			if err := s.SetTagMute(ctx, tc.tags[0], until); err != nil {
				t.Fatal(err)
			}
			for range tc.row {
				m, _ = press(t, m, keyDown)
			}
			m, _ = press(t, m, keyE)
			requireText(t, m, "Tags: "+config.FormatTags(tc.tags))
			m, cmd := press(t, m, keyEnter)
			m = finishCommand(t, m, cmd)
			if got := tagsOf(t, s, tc.name); !reflect.DeepEqual(got, tc.tags) {
				t.Fatalf("edit stored tags %q, want %q", got, tc.tags)
			}
			mutes, err := s.TagMutes(ctx)
			if err != nil || len(mutes) != 1 || mutes[0].Tag != tc.tags[0] || !mutes[0].Until.Equal(until) {
				t.Fatalf("tag mutes after edit = %+v %v", mutes, err)
			}
			m, _ = press(t, m, keyY)
			requireText(t, m, "Tags: "+config.FormatTags(tc.tags))
			m, cmd = press(t, m, keyEnter)
			finishCommand(t, m, cmd)
			if got := tagsOf(t, s, tc.name+" copy"); !reflect.DeepEqual(got, tc.tags) {
				t.Fatalf("duplicate stored tags %q, want %q", got, tc.tags)
			}
		})
	}
}

func TestEditRemovesTags(t *testing.T) {
	s, m := tagStore(t)
	m, _ = press(t, m, keyE)
	m = focusRow(t, m, "Tags")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	m = replaceField(t, m, "nas, home", "")
	m, cmd := press(t, m, keyEnter)
	finishCommand(t, m, cmd)
	if got := tagsOf(t, s, "api"); len(got) != 0 {
		t.Fatalf("stored tags %q, want none", got)
	}
}

func tagSnapshot() Snapshot {
	return Snapshot{HasBeat: true, LastBeat: now, Services: []Service{
		{Check: config.Check{Name: "api", URL: "https://api.example.com", Tags: []string{"prod"}}},
		{Check: config.Check{Name: "nas-smb", Kind: config.CheckTCP, Host: "nas", Port: 445, Tags: []string{"nas", "home"}}},
		{Check: config.Check{Name: "nas-web", URL: "https://nas.home.arpa:5001", Tags: []string{"nas", "home"}}},
		{Check: config.Check{Name: "status-page", URL: "https://status.example.com"}},
	}}
}

func TestListShowsTagsWideAndShortWhenNarrow(t *testing.T) {
	wide := renderList(tagSnapshot(), 0, now, nil, 160)
	for _, want := range []string{"api              #prod       PENDING", "nas-smb     tcp  #nas #home  PENDING", "nas-web          #nas #home  PENDING", "status-page                  PENDING"} {
		if !strings.Contains(ansi.Strip(wide), want) {
			t.Fatalf("wide list lacks %q:\n%s", want, ansi.Strip(wide))
		}
	}
	// Tags are kept before the trend and the tcp tag, shortened last.
	for _, tc := range []struct {
		width int
		wants []string
	}{
		{80, []string{"api          #prod       PENDING", "nas-smb      #nas #home  PENDING"}},
		{70, []string{"api          #prod    PENDING", "nas-smb      #nas +1  PENDING", "status-page           PENDING"}},
	} {
		narrow := ansi.Strip(renderList(tagSnapshot(), 0, now, nil, tc.width))
		for _, want := range tc.wants {
			if !strings.Contains(narrow, want) {
				t.Fatalf("%d-column list lacks %q:\n%s", tc.width, want, narrow)
			}
		}
		for _, line := range strings.Split(narrow, "\n") {
			if strings.Contains(line, "PENDING") && ansi.StringWidth(line) > tc.width {
				t.Fatalf("line wider than %d: %q", tc.width, line)
			}
		}
	}
}

func TestSearchByTag(t *testing.T) {
	m := modelWith(tagSnapshot())
	m.width, m.height = 160, 50
	m, _ = press(t, m, keySlash)
	m = typeText(t, m, "#")
	requireText(t, m, "tags: home · nas · prod · esc clear", "status-page")
	m = typeText(t, m, "nas")
	screen := view(m)
	if !strings.Contains(screen, "nas-smb") || !strings.Contains(screen, "nas-web") || strings.Contains(screen, "status-page") || strings.Contains(screen, "#prod") {
		t.Fatalf("#nas should show only nas-smb and nas-web:\n%s", screen)
	}
	requireText(t, m, "2 of 4")
	m, _ = press(t, m, keyBack)
	requireText(t, m, "tags: nas · esc clear")
	// Plain text still matches the name, URL and host, never a tag.
	m, _ = press(t, m, keyEsc, keySlash)
	m = typeText(t, m, "prod")
	requireText(t, m, "0 of 4")
	m, _ = press(t, m, keyBack, keyBack, keyBack, keyBack)
	m = typeText(t, m, "status")
	requireText(t, m, "1 of 4", "status-page")
}

func TestMuteTagTarget(t *testing.T) {
	s, m := tagStore(t)
	m.width, m.height = 160, 50
	m, _ = press(t, m, keyM)
	requireText(t, m, "Mute alerts · api", "Mute ‹ api ›", "←/→ target · ↑/↓ choose · enter mute · esc cancel")
	left, width := modalBorder(t, m, "Mute alerts · api")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "Mute alerts · #nas", "Mute ‹ #nas ›  covers api, db and any tagged later",
		"If a monitor is still down when the mute ends, you get the alert then.")
	if l, w := modalBorder(t, m, "Mute alerts · #nas"); l != left || w != width {
		t.Fatalf("the target moved the popup: %d/%d -> %d/%d", left, width, l, w)
	}
	m, _ = press(t, m, keyRight)
	requireText(t, m, "Mute ‹ #home ›  covers api, job and any tagged later")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "Mute ‹ api ›")
	m, _ = press(t, m, keyLeft, keyLeft)
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	ctx := context.Background()
	mutes, err := s.TagMutes(ctx)
	if err != nil || len(mutes) != 1 || mutes[0].Tag != "nas" || !mutes[0].Until.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("tag mutes = %+v %v, want #nas for 30m", mutes, err)
	}
	if mute, ok, _ := s.Mute(ctx, "db"); !ok || mute.Tag != "nas" {
		t.Fatalf("db mute = %+v %v, want through #nas", mute, ok)
	}
	if strings.Count(view(m), "⏸ 30m left") != 2 {
		t.Fatalf("api and db should show the mute:\n%s", view(m))
	}
}

func TestMuteWithoutTagsHasNoTargetRow(t *testing.T) {
	_, m := mutedModel(t, check.StatusUp)
	m, _ = press(t, m, keyM)
	if strings.Contains(view(m), "Mute ‹") || strings.Contains(view(m), "←/→ target") {
		t.Fatalf("an untagged monitor has no target choice:\n%s", view(m))
	}
}

func TestUnmuteThroughTagAsksFirst(t *testing.T) {
	s, m := tagStore(t)
	ctx := context.Background()
	if err := s.SetTagMute(ctx, "nas", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	m.width, m.height = 160, 50
	m, cmd := press(t, m, keyU)
	if cmd != nil {
		t.Fatal("u unmuted the tag without asking")
	}
	requireText(t, m, "Unmute #nas", "api is muted through #nas.", "Unmuting ends the #nas mute for api and db.", "Mutes set on a single monitor stay.", "y unmute · n cancel")
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if strings.Contains(view(m), "Unmute #nas") {
		t.Fatal("n did not close the question")
	}
	if mutes, _ := s.TagMutes(ctx); len(mutes) != 1 {
		t.Fatalf("n unmuted: %+v", mutes)
	}
	m, _ = press(t, m, keyU)
	m, cmd = press(t, m, keyY)
	m = finishCommand(t, m, cmd)
	if mutes, _ := s.TagMutes(ctx); len(mutes) != 0 {
		t.Fatalf("y left %+v", mutes)
	}
	if strings.Contains(view(m), "⏸") || strings.Contains(view(m), "Unmute #nas") {
		t.Fatalf("still muted or asking:\n%s", view(m))
	}
}

func TestTagMaintenanceWindow(t *testing.T) {
	s, m := tagStore(t)
	m.width, m.height = 160, 50
	ctx := context.Background()
	m, _ = press(t, m, keyW, keyA)
	requireText(t, m, "> Applies to   ‹ api ›", "←/→ day or target")
	left, width := modalBorder(t, m, "Maintenance · api")
	m, _ = press(t, m, keyRight)
	requireText(t, m, "> Applies to   ‹ #nas ›   covers api, db and any tagged later")
	if l, w := modalBorder(t, m, "Maintenance · api"); l != left || w != width {
		t.Fatalf("the target moved the popup: %d/%d -> %d/%d", left, width, l, w)
	}
	// Sunday (the last day) for 2h from 03:00: active now.
	m, _ = press(t, m, keyTab)
	m, _ = press(t, m, keyLeft, keyLeft, keyLeft, keyLeft, keyLeft, keyLeft, keyRight, keyRight, keyRight, keyRight, keyRight, keyRight, keySpace, keyTab)
	m = replaceField(t, m, "02:00", "03:00")
	m, _ = press(t, m, keyTab)
	m = replaceField(t, m, "1h", "2h")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	windows, err := s.TagMaintenanceWindows(ctx, "nas")
	if err != nil || len(windows) != 1 || windows[0].Weekdays[0] != time.Sunday || windows[0].Duration != 2*time.Hour {
		t.Fatalf("#nas windows = %+v %v", windows, err)
	}
	if own, _ := s.MaintenanceWindows(ctx, "payments"); len(own) != 0 {
		t.Fatalf("payments got %+v", own)
	}
	requireText(t, m, "Sun 03:00 + 2h · UTC   #nas")
	m, _ = press(t, m, keyEsc)
	if strings.Count(view(m), "◷ until 05:00") != 2 {
		t.Fatalf("api and db should be held:\n%s", view(m))
	}

	// A member edits the tag's window; the target cannot change.
	m, _ = press(t, m, keyDown, keyW)
	requireText(t, m, "Maintenance · db", "Sun 03:00 + 2h · UTC   #nas")
	m, _ = press(t, m, keyE)
	requireText(t, m, "Edit maintenance window", "Applies to   #nas")
	m, _ = press(t, m, keyTab, keyTab)
	m = replaceField(t, m, "2h", "3h")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	if windows, _ = s.TagMaintenanceWindows(ctx, "nas"); len(windows) != 1 || windows[0].Duration != 3*time.Hour {
		t.Fatalf("edited #nas windows = %+v", windows)
	}
	m, _ = press(t, m, keyD)
	requireText(t, m, "Delete the #nas maintenance window Sun 03:00 + 3h · UTC?", "It applies to every monitor tagged nas.")
	m, cmd = press(t, m, keyY)
	finishCommand(t, m, cmd)
	if windows, _ = s.TagMaintenanceWindows(ctx, "nas"); len(windows) != 0 {
		t.Fatalf("deleted #nas windows = %+v", windows)
	}
}

func TestDetailShowsTagsAndHoldsThroughTag(t *testing.T) {
	s, m := tagStore(t)
	ctx := context.Background()
	if err := s.SetTagMute(ctx, "nas", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Tag: "home", Weekdays: []time.Weekday{time.Sunday}, StartMinute: 3 * 60, Duration: 2 * time.Hour, Zone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMaintenanceWindow(ctx, config.MaintenanceWindow{Check: "api", Weekdays: []time.Weekday{time.Saturday}, StartMinute: 2 * 60, Duration: time.Hour, Zone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	m.width, m.height = 160, 60
	m, _ = press(t, m, keyEnter)
	requireText(t, m,
		"Muted        until 05:45 (2h left) · through #nas · nothing pending",
		"Maintenance  until 05:00 (1h 15m left) · through #home · nothing pending",
		"Tags         #nas #home",
		"Maintenance  2 windows · 1 through #home (w to edit)")
}

// renderAt draws m at width x height and fails when there are too many
// lines or a popup or list row (up to its reason, which the terminal cuts as
// before tags) is too wide.
func renderAt(t *testing.T, m Model, width, height int) string {
	t.Helper()
	next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	screen := view(next.(Model))
	lines := strings.Split(screen, "\n")
	if len(lines) > height {
		t.Fatalf("%d lines at %dx%d:\n%s", len(lines), width, height, screen)
	}
	for _, line := range lines {
		line = strings.TrimSuffix(strings.TrimRight(line, " "), "needs --api-listen")
		if ansi.StringWidth(strings.TrimRight(line, " ")) > width {
			t.Fatalf("line wider than %d: %q\n%s", width, line, screen)
		}
	}
	return screen
}

func TestTagScreensFit80x24(t *testing.T) {
	s, m := tagStore(t)
	if err := s.SetTagMute(context.Background(), "nas", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	m = reload(t, m)
	m.width, m.height = 80, 24
	list := renderAt(t, m, 80, 24)
	if !strings.Contains(list, "api       #nas #home  UP") {
		t.Fatalf("80x24 list lacks the tags:\n%s", list)
	}
	form, _ := press(t, m, keyE)
	form = focusRow(t, form, "Tags")
	if screen := renderAt(t, form, 80, 24); !strings.Contains(screen, "╭─ Edit monitor") || !strings.Contains(screen, "Tags: nas, home│") {
		t.Fatalf("form is not a popup at 80x24:\n%s", screen)
	}
	mute, _ := press(t, m, keyM, keyRight)
	if screen := renderAt(t, mute, 80, 24); !strings.Contains(screen, "╭─ Mute alerts · #nas") || !strings.Contains(screen, "Mute ‹ #nas ›") {
		t.Fatalf("mute is not a popup at 80x24:\n%s", screen)
	}
	unmute, _ := press(t, m, keyU)
	if screen := renderAt(t, unmute, 80, 24); !strings.Contains(screen, "╭─ Unmute #nas") {
		t.Fatalf("unmute question is not a popup at 80x24:\n%s", screen)
	}
	maint, _ := press(t, m, keyW, keyA, keyRight)
	// The window form's weekday row is too wide for a popup at 80 columns
	// with or without tags, so it takes the screen there as before.
	if screen := renderAt(t, maint, 80, 24); !strings.Contains(screen, "Applies to   ‹ #nas ›   covers api, db and any tagged later") {
		t.Fatalf("maintenance window form at 80x24:\n%s", screen)
	}
	detail, _ := press(t, m, keyEnter)
	if screen := renderAt(t, detail, 80, 24); !strings.Contains(screen, "through #nas") {
		t.Fatalf("detail at 80x24 lacks the tag mute:\n%s", screen)
	}
}
