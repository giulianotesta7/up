package alert

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// Channel test processing limits.
const (
	// ChannelTestTimeout bounds one test delivery.
	ChannelTestTimeout = 30 * time.Second
	// ChannelTestPollInterval is how often the daemon looks for new requests,
	// like monitor discovery.
	ChannelTestPollInterval = 2 * time.Second
	// completeTimeout bounds recording a test outcome, which must still happen
	// after the daemon was asked to stop.
	completeTimeout = 5 * time.Second
)

// SendTest sends a test notification through the channel called name and
// returns when the transport has answered, with its error if any. It touches
// no check, result, streak, mute or certificate state: the event has no check
// and no result, only the channel to look up.
func (n *Notifier) SendTest(ctx context.Context, name string) error {
	return n.deliver(ctx, event{check: config.Check{Channel: name}, test: true})
}

// testMessage is the email sent by a channel test, labelled as a test in both
// subject and body.
func testMessage(ch config.Channel, from string, hostname string, at time.Time) Message {
	return Message{
		From:    from,
		To:      ch.Recipients,
		Date:    at,
		Subject: "[up] TEST notification from channel " + ch.Name,
		Body: fmt.Sprintf("This is a test notification from up, requested for channel %s.\n"+
			"It was sent by hand; no monitor was checked and no monitor data changed.\n"+
			"Host:   %s\n", ch.Name, hostname),
	}
}

// TestQueue is the channel test request queue. *store.Store implements it.
type TestQueue interface {
	FailSendingChannelTests(ctx context.Context, reason string, at time.Time) (int64, error)
	ClaimChannelTest(ctx context.Context, at time.Time) (config.ChannelTest, bool, error)
	CompleteChannelTest(ctx context.Context, id int64, errText string, at time.Time) error
}

// TestRunner processes channel test requests for the daemon.
type TestRunner struct {
	Queue    TestQueue
	Notifier *Notifier
	Logger   *slog.Logger
	// Now, Timeout and Interval default to time.Now, ChannelTestTimeout and
	// ChannelTestPollInterval.
	Now      func() time.Time
	Timeout  time.Duration
	Interval time.Duration
}

func (r TestRunner) defaults() TestRunner {
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Timeout <= 0 {
		r.Timeout = ChannelTestTimeout
	}
	if r.Interval <= 0 {
		r.Interval = ChannelTestPollInterval
	}
	return r
}

// Run fails the requests a previous daemon left sending, then processes
// pending requests one at a time until ctx is cancelled. A request being sent
// at shutdown is recorded as failed instead of staying sending.
func (r TestRunner) Run(ctx context.Context) {
	r = r.defaults()
	if _, err := r.Queue.FailSendingChannelTests(ctx, "interrupted by a daemon restart", r.Now()); err != nil && ctx.Err() == nil {
		r.Logger.Error("failing interrupted channel tests failed", "error", err)
	}
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		for r.processOne(ctx) {
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// processOne claims, sends and completes one request; it reports whether one
// was claimed.
func (r TestRunner) processOne(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	t, ok, err := r.Queue.ClaimChannelTest(ctx, r.Now())
	if err != nil {
		if ctx.Err() == nil {
			r.Logger.Error("claiming channel test failed", "error", err)
		}
		return false
	}
	if !ok {
		return false
	}
	sendCtx, cancel := context.WithTimeout(ctx, r.Timeout)
	err = r.Notifier.SendTest(sendCtx, t.Channel)
	cancel()
	text := ""
	if err != nil {
		text = store.SafeTestError(err.Error())
		if ctx.Err() != nil {
			text = "interrupted: the daemon was stopping"
		}
		text = strings.TrimSpace(text)
	}
	done, cancelDone := context.WithTimeout(context.WithoutCancel(ctx), completeTimeout)
	defer cancelDone()
	if err := r.Queue.CompleteChannelTest(done, t.ID, text, r.Now()); err != nil {
		r.Logger.Error("recording channel test failed", "channel", t.Channel, "error", err)
	}
	return ctx.Err() == nil
}
