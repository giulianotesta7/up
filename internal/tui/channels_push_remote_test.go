package tui_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/heohlee/up/internal/client"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/tui"
)

// TestRemotePushChannelCreateEditAndList drives the channel form against the
// remote API at 80x24: the token is sealed to the server's key, the list
// shows SENDS TO and the edit form shows the token as set.
func TestRemotePushChannelCreateEditAndList(t *testing.T) {
	s, url := remoteServer(t)
	ctx := context.Background()
	k, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicKey(ctx, k.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := s.Beat(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	m := tui.New(client.New(url, remoteToken), time.Now, tui.Options{Destination: url})
	m, _ = remoteUpdate(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m, _ = remoteUpdate(t, m, m.Init()())
	right, tab := tea.KeyPressMsg{Code: tea.KeyRight}, tea.KeyPressMsg{Code: tea.KeyTab}
	typeText := func(text string) { m, _ = remoteUpdate(t, m, tea.KeyPressMsg{Text: text}) }
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"}, tea.KeyPressMsg{Code: 'n', Text: "n"})
	typeText("oncall-tg")
	m, _ = remotePress(t, m, tab, right, right, right, tab)
	typeText("123456:SECRET-tok")
	m, _ = remotePress(t, m, tab)
	typeText("-1001234567890")
	out := remoteScreen(m)
	if !strings.Contains(out, "╭─ New channel") || !strings.Contains(out, "Kind: ‹ telegram ›") || strings.Contains(out, "SECRET") {
		t.Fatalf("form:\n%s", out)
	}
	for _, line := range strings.Split(m.View().Content, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatalf("line wider than 80: %q", line)
		}
	}
	m, cmd := remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, cmd = remoteUpdate(t, m, cmd())
	m, _ = remoteUpdate(t, m, cmd())
	list, err := s.Channels(ctx)
	if err != nil || len(list) != 1 || list[0].ChatID != "-1001234567890" {
		t.Fatalf("remote create: %+v %v", list, err)
	}
	if got, err := k.Open(list[0].Token); err != nil || got != "123456:SECRET-tok" {
		t.Fatalf("token = %q %v", got, err)
	}
	out = remoteScreen(m)
	if !strings.Contains(out, "oncall-tg") || !strings.Contains(out, "telegram · chat -1001234567890") || strings.Contains(out, "SECRET") {
		t.Fatalf("list:\n%s", out)
	}
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: 'e', Text: "e"}, tab)
	if out := remoteScreen(m); !strings.Contains(out, "> Bot token: set ‹ keep ›") {
		t.Fatalf("edit form:\n%s", out)
	}
	m, _ = remotePress(t, m, tab)
	m, _ = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	typeText("1")
	m, cmd = remotePress(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m, cmd = remoteUpdate(t, m, cmd())
	_, _ = remoteUpdate(t, m, cmd())
	list, err = s.Channels(ctx)
	if err != nil || len(list) != 1 || list[0].ChatID != "-1001234567891" {
		t.Fatalf("remote edit: %+v %v", list, err)
	}
	if got, _ := k.Open(list[0].Token); got != "123456:SECRET-tok" {
		t.Fatalf("keep changed the token: %q", got)
	}
}
