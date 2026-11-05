package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// Copy shown by the channel test popup.
const (
	testAcceptedSMTP = "Accepted by the mail server; inbox delivery is not confirmed."
	testAcceptedHook = "Accepted (HTTP 2xx)"
	testRunning      = "Sending test…"
	testAlready      = "A test is already running for this channel."
	testNoDaemon     = "Daemon not running; start `up serve` (or reconnect to the server) and retry."
)

// channelTestPrompt is the "Test channel" popup over the channel list. The
// request is only enqueued here; the daemon sends it, and the outcome comes
// back in later snapshots as Snapshot.ChannelTests.
type channelTestPrompt struct {
	name string
	// id is the request this popup follows; 0 until one was enqueued.
	id      int64
	queuing bool
	err     string
}

type channelTestMsg struct {
	generation uint64
	name       string
	test       config.ChannelTest
	err        error
}

// testAlreadyActive recognizes the store's ErrExists, locally or as the text a
// server returned for its 409.
func testAlreadyActive(err error) bool {
	return errors.Is(err, store.ErrExists) || strings.Contains(err.Error(), "already in progress")
}

func (m Model) handleChannelTestKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := *m.channelTest
	m.channelTest = &p
	switch msg.String() {
	case "esc", "q":
		m.channelTest = nil
	case "enter", "t":
		if p.queuing {
			return m, nil
		}
		tester, ok := m.store.(ChannelTester)
		switch {
		case !ok:
			p.err = "Channel tests are not available with this store."
		case m.snap.Stale(m.now()):
			// Nothing would send it: do not leave a request waiting.
			p.err = testNoDaemon
		default:
			p.queuing, p.err, p.id = true, "", 0
			m.generation++
			generation, name, now := m.generation, p.name, m.now()
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
				defer cancel()
				t, err := tester.EnqueueChannelTest(ctx, name, now)
				return channelTestMsg{generation: generation, name: name, test: t, err: err}
			}
		}
	}
	return m, nil
}

// channelTestResult applies the outcome of enqueueing a request.
func (m Model) channelTestResult(msg channelTestMsg) (tea.Model, tea.Cmd) {
	if msg.generation != m.generation || m.channelTest == nil || m.channelTest.name != msg.name || !m.channelTest.queuing {
		return m, nil
	}
	p := *m.channelTest
	p.queuing = false
	switch {
	case msg.err == nil:
		p.id = msg.test.ID
	case testAlreadyActive(msg.err):
		p.err = testAlready
	default:
		p.err = "Cannot send test: " + msg.err.Error()
	}
	m.channelTest = &p
	return m, m.load
}

// followedTest is the request the popup shows, if the snapshot has it yet.
func followedTest(snap Snapshot, p channelTestPrompt) (config.ChannelTest, bool) {
	for _, t := range snap.ChannelTests {
		if t.Channel == p.name && p.id != 0 && t.ID >= p.id {
			return t, true
		}
	}
	return config.ChannelTest{}, false
}

func renderChannelTest(p channelTestPrompt, snap Snapshot, now time.Time, loadErr error) string {
	kind := ""
	for _, c := range snap.Channels {
		if c.Name == p.name {
			kind = c.Kind
		}
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("Test channel") + "\n" + renderBanners(snap, now, loadErr))
	b.WriteString("Channel: " + selectedNameStyle.Render(displayText(p.name)) + " " + helpStyle.Render("("+displayText(kind)+")") + "\n\n")
	active := false
	t, seen := followedTest(snap, p)
	switch {
	case p.err != "":
		b.WriteString(warnStyle.Render(p.err))
	case p.queuing || (p.id != 0 && !seen) || (seen && t.Status.Active()):
		active = true
		if (seen || p.id != 0) && !p.queuing && (snap.Stale(now) || loadErr != nil) {
			b.WriteString(warnStyle.Render(testNoDaemon + " The request stays queued."))
		} else {
			b.WriteString(accentStyle.Render(testRunning))
		}
	case seen && t.Status == config.TestOK:
		text := testAcceptedSMTP
		if kind == config.KindWebhook {
			text = testAcceptedHook
		}
		b.WriteString(upStyle.Render("✓ " + text))
	case seen:
		b.WriteString(downStyle.Render("✗ Test failed: " + displayText(t.Error)))
	default:
		b.WriteString(helpStyle.Render("Press enter to send a test."))
	}
	b.WriteString("\n\n")
	if active {
		b.WriteString(renderHelp("esc close · the test continues · ctrl+c quit"))
	} else {
		b.WriteString(renderHelp("enter send test · esc close · ctrl+c quit"))
	}
	return b.String()
}
