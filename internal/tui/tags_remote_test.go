package tui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/heohlee/up/internal/client"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/remote"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

// tagServer serves a store with api and db tagged nas through the remote
// API routes the TUI writes tags, mutes and maintenance windows with.
func tagServer(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	for _, c := range []config.Check{
		{Name: "api", URL: "https://api.example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200, Tags: []string{"nas"}},
		{Name: "db", Kind: config.CheckTCP, Host: "10.0.0.5", Port: 5432, Interval: time.Minute, Timeout: 2 * time.Second, Tags: []string{"nas"}},
	} {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Beat(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := remote.SaveToken(filepath.Join(dir, "token"), remoteToken); err != nil {
		t.Fatal(err)
	}
	verifier, err := remote.LoadVerifier(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/snapshot", remote.SnapshotRoutes(func(ctx context.Context, now time.Time) (tui.Snapshot, error) { return tui.Load(ctx, s, now) }))
	writes := remote.WriteRoutes(s)
	mux.Handle("/api/v1/checks", writes)
	mux.Handle("/api/v1/checks/", writes)
	maintenance := remote.MaintenanceRoutes(s)
	mux.Handle("/api/v1/checks/{name}/maintenance", maintenance)
	mux.Handle("/api/v1/checks/{name}/maintenance/{id}", maintenance)
	tags := remote.TagRoutes(s)
	for _, p := range remote.TagPatterns {
		mux.Handle(p, tags)
	}
	srv := httptest.NewServer(remote.NewHandler(remote.Options{Verifier: verifier, Version: "test", Routes: mux}))
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func TestRemoteTagsMuteAndMaintenance(t *testing.T) {
	s, address := tagServer(t)
	ctx := context.Background()
	m := tui.New(client.New(address, remoteToken), time.Now, tui.Options{Destination: address})
	m, _ = remoteUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = remoteUpdate(t, m, m.Init()())
	key := func(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: []rune(s)[0], Text: s} }
	tab, right, enter := tea.KeyPressMsg{Code: tea.KeyTab}, tea.KeyPressMsg{Code: tea.KeyRight}, tea.KeyPressMsg{Code: tea.KeyEnter}
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf(format+"\n%s", append(args, remoteScreen(m))...)
	}

	// The edit form keeps the tags and adds one.
	m, _ = remotePress(t, m, key("e"))
	for i := 0; !strings.Contains(remoteScreen(m), "> Tags: nas"); i++ {
		if i > 20 {
			fail("no Tags row")
		}
		m, _ = remotePress(t, m, tab)
	}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEnd}, tea.KeyPressMsg{Text: ", home"})
	m, cmd := remotePress(t, m, enter)
	m = settle(t, m, cmd)
	checks, err := s.Checks(ctx)
	if err != nil || !reflect.DeepEqual(checks[0].Tags, []string{"nas", "home"}) {
		fail("stored %+v %v", checks, err)
	}
	if !strings.Contains(remoteScreen(m), "#nas #home") {
		fail("list lacks the tags")
	}

	// Mute #nas, then unmute it from db after the question.
	m, _ = remotePress(t, m, key("m"), right)
	if !strings.Contains(remoteScreen(m), "Mute ‹ #nas ›  covers api, db and monitors tagged nas later") {
		fail("no tag target")
	}
	m, cmd = remotePress(t, m, enter)
	m = settle(t, m, cmd)
	if mutes, err := s.TagMutes(ctx); err != nil || len(mutes) != 1 || mutes[0].Tag != "nas" {
		fail("tag mutes %+v %v", mutes, err)
	}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyDown}, key("u"))
	if !strings.Contains(remoteScreen(m), "db is muted through #nas.") {
		fail("u did not ask")
	}
	m, cmd = remotePress(t, m, key("y"))
	m = settle(t, m, cmd)
	if mutes, err := s.TagMutes(ctx); err != nil || len(mutes) != 0 {
		fail("tag mutes after unmute %+v %v", mutes, err)
	}

	// A #nas maintenance window added, edited and deleted from db.
	m, _ = remotePress(t, m, key("w"), key("a"), right, tab, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m, cmd = remotePress(t, m, enter)
	m = settle(t, m, cmd)
	windows, err := s.TagMaintenanceWindows(ctx, "nas")
	if err != nil || len(windows) != 1 || windows[0].Weekdays[0] != time.Monday {
		fail("#nas windows %+v %v", windows, err)
	}
	if own, _ := s.MaintenanceWindows(ctx, "db"); len(own) != 1 || own[0].Tag != "nas" {
		fail("db windows %+v", own)
	}
	m, _ = remotePress(t, m, key("e"), tab, tab, tea.KeyPressMsg{Code: tea.KeyBackspace}, tea.KeyPressMsg{Code: tea.KeyBackspace}, tea.KeyPressMsg{Text: "2h"})
	m, cmd = remotePress(t, m, enter)
	m = settle(t, m, cmd)
	if windows, _ = s.TagMaintenanceWindows(ctx, "nas"); len(windows) != 1 || windows[0].Duration != 2*time.Hour {
		fail("edited #nas windows %+v", windows)
	}
	m, _ = remotePress(t, m, key("d"))
	m, cmd = remotePress(t, m, key("y"))
	m = settle(t, m, cmd)
	if windows, _ = s.TagMaintenanceWindows(ctx, "nas"); len(windows) != 0 {
		fail("deleted #nas windows %+v", windows)
	}
}
