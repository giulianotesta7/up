package tui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/heohlee/up/internal/client"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/remote"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

// heartbeatServer serves a store with its key through the remote API with
// the heartbeat token route, as `up serve --api-listen :8080` does. It
// returns the store, its database path and the server URL.
func heartbeatServer(t *testing.T) (*store.Store, string, string) {
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
	ctx := context.Background()
	if err := s.SetPublicKey(ctx, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAPIListener(ctx, config.APIListener{Address: ":8080"}); err != nil {
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
	mux.Handle("/api/v1/checks/{name}/heartbeat-token", remote.HeartbeatTokenRoutes(s, key))
	srv := httptest.NewServer(remote.NewHandler(remote.Options{Verifier: verifier, Version: "test", Routes: mux}))
	t.Cleanup(srv.Close)
	return s, path, srv.URL
}

// settle runs cmd and, after a save, the snapshot read that follows (not
// the poll tick after it).
func settle(t *testing.T, m tui.Model, cmd tea.Cmd) tui.Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	m, next := remoteUpdate(t, m, cmd())
	if next != nil {
		m, _ = remoteUpdate(t, m, next())
	}
	return m
}

func serverToken(t *testing.T, s *store.Store, path, name string) string {
	t.Helper()
	token, err := tui.NewLocalHeartbeatTokens(s, path).RevealHeartbeatToken(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// TestRemoteHeartbeatURLRevealAndNewToken logs in through a proxy URL while
// the daemon listens on :8080: every ping URL is built from the login URL,
// with its path prefix, never the daemon's port.
func TestRemoteHeartbeatURLRevealAndNewToken(t *testing.T) {
	for _, login := range []string{"https://up.example.com", "https://example.com/up"} {
		t.Run(login, func(t *testing.T) { remoteHeartbeatFlow(t, login) })
	}
}

func remoteHeartbeatFlow(t *testing.T, login string) {
	s, path, address := heartbeatServer(t)
	u, _ := url.Parse(login)
	clock := time.Date(2026, 10, 25, 3, 45, 0, 0, time.UTC)
	m := tui.New(client.New(address, remoteToken), func() time.Time { return clock }, tui.Options{Destination: login, Hostname: u.Host})
	m, _ = remoteUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m, _ = remoteUpdate(t, m, m.Init()())
	base := login + "/api/v1/heartbeat/"

	right, tab := tea.KeyPressMsg{Code: tea.KeyRight}, tea.KeyPressMsg{Code: tea.KeyTab}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"}, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, right, right, tab,
		tea.KeyPressMsg{Text: "nightly"})
	if !strings.Contains(remoteScreen(m), "Type: ‹ Heartbeat ›") || strings.Contains(remoteScreen(m), "No API listener") {
		t.Fatalf("form:\n%s", remoteScreen(m))
	}
	m, cmd := remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = settle(t, m, cmd)
	token := serverToken(t, s, path, "nightly")
	if !strings.Contains(remoteScreen(m), "Ping URL · nightly") || !strings.Contains(remoteScreen(m), "URL    "+base+token) {
		t.Fatalf("no ping URL after saving:\n%s", remoteScreen(m))
	}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if strings.Contains(remoteScreen(m), token) {
		t.Fatalf("esc left the token:\n%s", remoteScreen(m))
	}

	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(remoteScreen(m), "Token        ••••••••") || strings.Contains(remoteScreen(m), token) {
		t.Fatalf("detail before v:\n%s", remoteScreen(m))
	}
	m, cmd = remotePress(t, m, tea.KeyPressMsg{Code: 'v', Text: "v"})
	m = settle(t, m, cmd)
	if !strings.Contains(remoteScreen(m), "URL          "+base) || !strings.Contains(remoteScreen(m), "Token        "+token) {
		t.Fatalf("v did not reveal through the server:\n%s", remoteScreen(m))
	}

	// From Period: Schedule, Type, then Token.
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: 'e', Text: "e"}, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift},
		tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, right, tea.KeyPressMsg{Code: tea.KeyEnter})
	if strings.Contains(remoteScreen(m), token) || !strings.Contains(remoteScreen(m), "Replace the token?") {
		t.Fatalf("confirm:\n%s", remoteScreen(m))
	}
	m, cmd = remotePress(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	m = settle(t, m, cmd)
	fresh := serverToken(t, s, path, "nightly")
	if fresh == token || !strings.Contains(remoteScreen(m), "Saved. The old URL no longer works.") || !strings.Contains(remoteScreen(m), "URL    "+base+fresh) {
		t.Fatalf("new token %q (old %q):\n%s", fresh, token, remoteScreen(m))
	}
	if found, _ := s.PingHeartbeat(context.Background(), token, false, clock); found {
		t.Fatal("the old token still works")
	}

	small := tui.New(client.New(address, remoteToken), func() time.Time { return clock }, tui.Options{Destination: login, Hostname: u.Host})
	small, _ = remoteUpdate(t, small, tea.WindowSizeMsg{Width: 80, Height: 24})
	small, _ = remoteUpdate(t, small, small.Init()())
	small, _ = remotePress(t, small, tea.KeyPressMsg{Code: tea.KeyEnter})
	small, cmd = remotePress(t, small, tea.KeyPressMsg{Code: 'v', Text: "v"})
	small = settle(t, small, cmd)
	if !strings.Contains(remoteScreen(small), "URL          "+base+"\n") || !strings.Contains(remoteScreen(small), "Token        "+fresh) || strings.Contains(remoteScreen(small), ":8080") {
		t.Fatalf("80x24 reveal:\n%s", remoteScreen(small))
	}
}
