package tui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/client"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/remote"
	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

const remoteToken = "up_" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE"

// remoteServer serves a real store through the remote API, as `up serve` does.
func remoteServer(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := remote.SaveToken(filepath.Join(dir, "token"), remoteToken); err != nil {
		t.Fatal(err)
	}
	verifier, err := remote.LoadVerifier(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/v1/snapshot", remote.SnapshotRoutes(func(ctx context.Context, now time.Time) (tui.Snapshot, error) { return tui.Load(ctx, s, now) }))
	alerts := remote.AlertWriteRoutes(s, nil)
	for _, p := range []string{"/api/v1/smtp-servers", "/api/v1/smtp-servers/", "/api/v1/channels", "/api/v1/channels/"} {
		mux.Handle(p, alerts)
	}
	mux.Handle("/api/v1/deliveries", remote.DeliveryRoutes(s))
	srv := httptest.NewServer(remote.NewHandler(remote.Options{Verifier: verifier, Version: "test", Routes: mux}))
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func remoteUpdate(t *testing.T, m tui.Model, msg tea.Msg) (tui.Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(tui.Model), cmd
}

func remotePress(t *testing.T, m tui.Model, keys ...tea.KeyPressMsg) (tui.Model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		m, cmd = remoteUpdate(t, m, k)
	}
	return m, cmd
}

func remoteScreen(m tui.Model) string { return ansi.Strip(m.View().Content) }

func TestRemoteNotificationsViewShowsTheServersDeliveries(t *testing.T) {
	s, url := remoteServer(t)
	ctx := context.Background()
	if err := s.Beat(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "mail-main", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ops-mail", "pager", "unused"} {
		if err := s.AddChannel(ctx, config.Channel{Name: name, Kind: config.KindEmail, SMTPServer: "mail-main", Recipients: []string{name + "@acme.io"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddCheck(ctx, config.Check{Name: "payouts", URL: "https://example.com", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200, Channel: "pager"}); err != nil {
		t.Fatal(err)
	}
	// Half a minute past the minute, so the client's skew correction never
	// changes the minute shown.
	base := time.Now().Add(-time.Hour).Truncate(time.Minute).Add(30 * time.Second)
	for _, a := range []store.Attempt{
		{At: base, Monitor: "payouts", Channel: "ops-mail", Event: store.EventDown, Outcome: store.OutcomeSent},
		{At: base.Add(2 * time.Minute), Monitor: "payouts", Channel: "pager", Event: store.EventDown, Outcome: store.OutcomeFailed, Reason: "status 502"},
	} {
		if err := s.RecordAttempt(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	clock := func(at time.Time) string { return at.Local().Format("Jan 02 15:04") }

	m := tui.New(client.New(url, remoteToken), time.Now, tui.Options{Destination: url})
	m, _ = remoteUpdate(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = remoteUpdate(t, m, m.Init()())
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	out := remoteScreen(m)
	lines := strings.Split(out, "\n")
	if len(lines) != 24 || !strings.HasSuffix(out, "● remote · "+url) {
		t.Fatalf("remote view has %d lines or lost its footer:\n%s", len(lines), out)
	}
	for _, want := range []string{"Mail servers    Channels    Deliveries", "LAST DELIVERY", "✓ sent " + clock(base), "✗ failed " + clock(base.Add(2*time.Minute)) + " · status 502"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	out = remoteScreen(m)
	newest, oldest := strings.Index(out, "status 502"), strings.Index(out, clock(base)+"  payouts  ops-mail")
	if newest < 0 || oldest < 0 || newest > oldest {
		t.Fatalf("deliveries not listed newest first:\n%s", out)
	}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, tea.KeyPressMsg{Code: 'j', Text: "j"}, tea.KeyPressMsg{Code: tea.KeyEnter})
	out = remoteScreen(m)
	if !strings.Contains(out, "channel pager · 1 of 2 · esc clears") || strings.Contains(out, "ops-mail  monitor.down") {
		t.Fatalf("enter on pager must show only its deliveries:\n%s", out)
	}
	// Forms keep writing to the server.
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}, tea.KeyPressMsg{Code: 'j', Text: "j"}, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if out := remoteScreen(m); !strings.Contains(out, `Delete channel "unused"?`) {
		t.Fatalf("delete prompt missing:\n%s", out)
	}
	m, cmd := remotePress(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	m, cmd = remoteUpdate(t, m, cmd())
	m, _ = remoteUpdate(t, m, cmd())
	channels, err := s.Channels(ctx)
	if err != nil || len(channels) != 2 {
		t.Fatalf("remote delete: %v %v", channels, err)
	}
	if out := remoteScreen(m); strings.Contains(out, "unused") || !strings.Contains(out, "LAST DELIVERY") {
		t.Fatalf("view after delete:\n%s", out)
	}
}
