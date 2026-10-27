package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChannelInvalidRecipientsAndRetry(t *testing.T) {
	m := channelServers(t)
	s := m.store
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, "ops")
	m, _ = press(t, m, keyTab, keyTab, keyTab)
	m = typeText(t, m, "invalid")
	m, cmd := press(t, m, keyEnter)
	if cmd == nil {
		t.Fatal("missing save")
	}
	busy, again := press(t, m, keyEnter, keyEsc, keyN)
	if again != nil {
		t.Fatal("saving accepted another operation")
	}
	m = finishCommand(t, busy, cmd)
	requireText(t, m, `Cannot save channel: store: add channel: recipient "invalid" is not a valid email address`)
	channels, err := s.Channels(context.Background())
	if err != nil || len(channels) != 0 {
		t.Fatalf("invalid save changed channels: %v %v", channels, err)
	}
	m = replaceField(t, m, "invalid", "ops@example.com")
	m, cmd = press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	requireText(t, m, "ops@example.com")
}
func TestChannelCancelDeleteAndQuit(t *testing.T) {
	m := channelServers(t)
	m, _ = press(t, m, keyC, keyN)
	m = typeText(t, m, "discard")
	m, _ = press(t, m, keyEsc)
	requireText(t, m, "No channels configured.")
	m, _ = press(t, m, keyN)
	m = typeText(t, m, "ops")
	m, _ = press(t, m, keyTab, keyTab, keyTab)
	m = typeText(t, m, "ops@example.com")
	m, cmd := press(t, m, keyEnter)
	m = finishCommand(t, m, cmd)
	for _, key := range []tea.KeyPressMsg{keyN, keyEsc} {
		current, _ := press(t, m, keyD, key)
		requireText(t, current, "Channels", "ops@example.com")
	}
	_, cmd = press(t, m, keyQ)
	if cmd == nil {
		t.Fatal("no quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("not quit")
	}
}
