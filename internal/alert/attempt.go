package alert

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

// AttemptRecorder stores delivery attempts. *store.Store implements it.
type AttemptRecorder interface {
	RecordAttempt(context.Context, store.Attempt) error
}

// trace is what one delivery learns on the way that the attempt record
// needs: the channel's webhook or server origin and every protected value it opened, so
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

// hidden is every form of the protected values a reason must not carry:
// each as it would be stored and base64 encoded with and without padding (an
// SMTP server may echo an AUTH response), longest first so no form is left
// half replaced.
func (t *trace) hidden() []string {
	var forms []string
	for _, v := range t.secrets {
		for _, f := range []string{store.FoldSpace(v), base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v))} {
			if f != "" && !slices.Contains(forms, f) {
				forms = append(forms, f)
			}
		}
	}
	slices.SortStableFunc(forms, func(a, b string) int { return len(b) - len(a) })
	return forms
}

// minHiddenAnywhere is the length (in runes) from which a protected value is
// hidden wherever it appears. A shorter one is hidden only where it stands
// alone, not touching a letter or digit: hidden inside ordinary words it
// would garble the reason and, by where the marks fall, give itself away.
const minHiddenAnywhere = 8

// hide replaces secret in text with a mark.
func hide(text, secret string) string {
	if utf8.RuneCountInString(secret) >= minHiddenAnywhere {
		return strings.ReplaceAll(text, secret, "[hidden]")
	}
	var b strings.Builder
	for {
		i := strings.Index(text, secret)
		if i < 0 {
			break
		}
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[i+len(secret):])
		if i > 0 && isWordRune(before) || i+len(secret) < len(text) && isWordRune(after) {
			_, size := utf8.DecodeRuneInString(text[i:])
			b.WriteString(text[:i+size])
			text = text[i+size:]
			continue
		}
		b.WriteString(text[:i])
		b.WriteString("[hidden]")
		text = text[i+len(secret):]
	}
	b.WriteString(text)
	return b.String()
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

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
// that is left, and names a webhook, discord, slack, ntfy or gotify channel
// by its origin only.
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
	// Scrub the text as it will be stored, so a value echoed across lines
	// cannot slip past.
	text = store.FoldSpace(text)
	if t != nil {
		for _, s := range t.hidden() {
			text = hide(text, s)
		}
	}
	text = urlText.ReplaceAllString(text, "[url]")
	if t == nil {
		return text
	}
	switch t.kind {
	case config.KindWebhook, config.KindDiscord, config.KindSlack, config.KindNtfy, config.KindGotify:
		text = t.kind + " " + t.origin + ": " + strings.TrimPrefix(text, t.kind+" ")
	case config.KindTelegram:
		text = "telegram: " + strings.TrimPrefix(text, "telegram ")
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
