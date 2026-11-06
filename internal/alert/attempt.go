package alert

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// AttemptRecorder stores delivery attempts. *store.Store implements it.
type AttemptRecorder interface {
	RecordAttempt(context.Context, store.Attempt) error
}

// trace is what one delivery learns on the way that the attempt record
// needs: the channel's webhook origin and every protected value it opened, so
// the reason can be scrubbed of them.
type trace struct {
	kind    string
	origin  string
	secrets []string
}

func (t *trace) know(values ...string) {
	if t == nil {
		return
	}
	for _, v := range values {
		if v != "" {
			t.secrets = append(t.secrets, v)
		}
	}
}

// name is the event's name in the delivery log.
func (e event) name() store.Event {
	switch {
	case e.cert != nil:
		return store.EventCert
	case e.result.Status == check.StatusDown:
		return store.EventDown
	}
	return store.EventRecovered
}

// recordAttempt stores the outcome of one delivery. Channel tests are not
// recorded. A failure to record is logged and never changes the delivery.
func (n *Notifier) recordAttempt(ctx context.Context, e event, t *trace, err error) {
	if n.opts.Attempts == nil || e.test {
		return
	}
	now := time.Now
	if n.opts.Now != nil {
		now = n.opts.Now
	}
	a := store.Attempt{At: now(), Monitor: e.check.Name, Channel: e.check.Channel, Event: e.name(), Outcome: store.OutcomeSent}
	if err != nil {
		a.Outcome, a.Reason = store.OutcomeFailed, failureReason(err, t)
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completeTimeout)
	defer cancel()
	if rerr := n.opts.Attempts.RecordAttempt(wctx, a); rerr != nil {
		n.opts.Logger.Error("recording alert attempt failed", "check", e.check.Name, "channel", e.check.Channel, "error", rerr)
	}
}

var urlText = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s/]*[^\s]*`)

// failureReason turns a delivery error into a short reason that carries no
// secret: transports already keep URLs, credentials and response bodies out
// of their errors, and this removes any opened protected value and any URL
// that is left, and names a webhook by its origin only.
func failureReason(err error, t *trace) string {
	var rejected *textproto.Error
	var text string
	switch {
	case errors.As(err, &rejected):
		text = fmt.Sprintf("mail server: %d %s", rejected.Code, rejected.Msg)
	case errors.Is(err, syscall.ECONNREFUSED):
		text = "connection refused"
	case errors.Is(err, context.DeadlineExceeded), isTimeout(err):
		text = "timed out"
	default:
		text = err.Error()
	}
	if t != nil {
		for _, s := range t.secrets {
			text = strings.ReplaceAll(text, s, "[hidden]")
		}
	}
	text = urlText.ReplaceAllString(text, "[url]")
	if t == nil {
		return text
	}
	switch t.kind {
	case config.KindWebhook:
		text = "webhook " + t.origin + ": " + strings.TrimPrefix(text, "webhook ")
	case config.KindEmail:
		if !strings.HasPrefix(text, "mail server") {
			text = "mail server: " + strings.TrimPrefix(text, "alert: ")
		}
	}
	return text
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
