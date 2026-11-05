package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

var keyT = tea.KeyPressMsg{Code: 't', Text: "t"}

// testChannelModel has a live daemon heartbeat and two channels, an email one
// (mail) and a webhook one (hook), on the channel screen with mail selected.
func testChannelModel(t *testing.T, beat bool) (*store.Store, Model) {
	t.Helper()
	ctx := context.Background()
	s, m := formStore(t)
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "smtp", Host: "localhost", Port: 25, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, config.Channel{Name: "mail", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, config.Channel{Name: "zhook", Kind: config.KindWebhook, WebhookURL: secret.Sealed{Key: []byte{1}, Data: []byte{2}}, WebhookOrigin: "https://hooks.example.com"}); err != nil {
		t.Fatal(err)
	}
	if beat {
		if err := s.Beat(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	next, _ := m.Update(m.Init()())
	m, _ = press(t, next.(Model), keyC)
	return s, m
}

func reload(t *testing.T, m Model) Model {
	t.Helper()
	next, _ := m.Update(m.Init()())
	return next.(Model)
}

func TestChannelTestPopupExplainsAndWaitsForEnter(t *testing.T) {
	s, m := testChannelModel(t, true)
	requireText(t, m, "t test")
	m, _ = press(t, m, keyT)
	requireText(t, m, "Test channel", "changes no monitor data", "mail", "Nothing is sent until you press enter", "enter send test")
	if _, ok, _ := s.LatestChannelTest(context.Background(), "mail"); ok {
		t.Fatal("opening the popup enqueued a test")
	}
	m, _ = press(t, m, keyEsc)
	requireText(t, m, "Channels", "mail")
	if strings.Contains(view(m), "Test channel") {
		t.Fatalf("popup still open:\n%s", view(m))
	}
}

func TestChannelTestSendsAndShowsOutcomes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		down   int
		errMsg string
		want   string
	}{
		{"email accepted", 0, "", "Accepted by the SMTP server; inbox delivery is not confirmed"},
		{"webhook accepted", 1, "", "Webhook accepted: HTTP 2xx"},
		{"failure", 0, "535 authentication failed", "Test failed: 535 authentication failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m := testChannelModel(t, true)
			channel := []string{"mail", "zhook"}[tc.down]
			if tc.down == 1 {
				m, _ = press(t, m, keyDown)
			}
			m, _ = press(t, m, keyT)
			m, cmd := press(t, m, keyEnter)
			m = finishCommand(t, m, cmd)
			requireText(t, m, "Sending test…")
			pending, ok, err := s.LatestChannelTest(ctx, channel)
			if err != nil || !ok || pending.Status != config.TestPending {
				t.Fatalf("enqueued test = %+v %v %v", pending, ok, err)
			}
			// The daemon's part, done by hand through the queue.
			claimed, _, _ := s.ClaimChannelTest(ctx, now)
			requireText(t, reload(t, m), "Sending test…")
			if err := s.CompleteChannelTest(ctx, claimed.ID, tc.errMsg, now); err != nil {
				t.Fatal(err)
			}
			requireText(t, reload(t, m), tc.want)
		})
	}
}

func TestChannelTestSecondRequestSaysAlreadyRunning(t *testing.T) {
	s, m := testChannelModel(t, true)
	m, _ = press(t, m, keyT)
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "A test is already running")
	n, err := s.CountChannelTests(context.Background(), "mail")
	if err != nil || n != 1 {
		t.Fatalf("tests = %d %v, want the second request rejected", n, err)
	}
}

func TestChannelTestWithoutDaemonSendsNothing(t *testing.T) {
	s, m := testChannelModel(t, false)
	m, _ = press(t, m, keyT)
	m, cmd := press(t, m, keyEnter)
	if cmd != nil {
		t.Fatal("a test was enqueued although the daemon is not running")
	}
	requireText(t, m, "Daemon not running")
	if _, ok, _ := s.LatestChannelTest(context.Background(), "mail"); ok {
		t.Fatal("a request was stored")
	}
}

func TestChannelTestQueuedRequestShowsDaemonStopped(t *testing.T) {
	_, m := testChannelModel(t, true)
	m, _ = press(t, m, keyT)
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	m.now = func() time.Time { return now.Add(10 * time.Minute) }
	requireText(t, reload(t, m), "Daemon not running")
}

// fakeTester is the remote client's view: a Store that also enqueues, with
// the server's conflict arriving as text.
type fakeTester struct {
	Store
	err error
}

func (f fakeTester) EnqueueChannelTest(context.Context, string, time.Time) (config.ChannelTest, error) {
	return config.ChannelTest{}, f.err
}

func TestChannelTestMapsRemoteErrors(t *testing.T) {
	_, m := testChannelModel(t, true)
	for _, tc := range []struct {
		err  error
		want string
	}{
		{errors.New(`store: enqueue channel test: a test of channel "mail" is already in progress`), "A test is already running"},
		{errors.New("server unreachable"), "Cannot send test: server unreachable"},
	} {
		r := m
		r.store = fakeTester{Store: m.store, err: tc.err}
		r, _ = press(t, r, keyT)
		r, cmd := press(t, r, keyEnter)
		r = finishCommand(t, r, cmd)
		requireText(t, r, tc.want)
	}
	r := m
	r.store = struct{ Store }{m.store}
	r, _ = press(t, r, keyT, keyEnter)
	requireText(t, r, "not available")
}

// PRESERVE: the other channel keys behave as before.
func TestChannelListKeysUnchangedByTestKey(t *testing.T) {
	_, m := testChannelModel(t, true)
	requireText(t, m, "n new", "e edit", "d delete", "esc back")
	e, _ := press(t, m, keyE)
	requireText(t, e, "Edit channel")
	d, _ := press(t, m, keyD)
	requireText(t, d, "Delete channel")
	n, _ := press(t, m, keyN)
	requireText(t, n, "New channel")
}
