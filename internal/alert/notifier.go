package alert

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

// Store is what the notifier reads. *store.Store implements it.
type Store interface {
	Channel(context.Context, string) (config.Channel, bool, error)
	SMTPServer(context.Context, string) (config.SMTPServer, bool, error)
	Outages(context.Context, string, int) ([]store.Outage, error)
}

// Sender delivers one message. SMTPSender implements it.
type Sender interface {
	Send(context.Context, config.SMTPServer, string, Message) error
}

// Options configures a Notifier. Location defaults to time.Local and Logger
// to slog.Default.
type Options struct {
	Store    Store
	Key      *secret.Key
	Sender   Sender
	Hostname string
	Logger   *slog.Logger
	Location *time.Location
}
type event struct {
	check     config.Check
	result    check.Result
	cert      *config.Cert
	remaining time.Duration
	// test marks a channel test notification. Transports label it as a test;
	// check and result are zero and no monitor state is involved.
	//lint:ignore U1000 set by the channel-test entry point (#59) and read by every transport
	test bool
}

// Notifier sends alerts from a bounded queue, one at a time and in order.
type Notifier struct {
	opts  Options
	queue chan event
}

// NewNotifier returns a Notifier; call Run to start delivering.
func NewNotifier(opts Options) *Notifier {
	if opts.Location == nil {
		opts.Location = time.Local
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Notifier{opts: opts, queue: make(chan event, 64)}
}

// Notify enqueues an alert without blocking.
func (n *Notifier) Notify(c config.Check, r check.Result) {
	n.enqueue(event{check: c, result: r})
}

// NotifyCert enqueues a certificate warning without blocking.
func (n *Notifier) NotifyCert(c config.Check, cert config.Cert, remaining time.Duration) {
	n.enqueue(event{check: c, cert: &cert, remaining: remaining})
}

func (n *Notifier) enqueue(e event) {
	select {
	case n.queue <- e:
	default:
		n.opts.Logger.Error("alert dropped: queue full", "check", e.check.Name)
	}
}

// Run delivers alerts in order until cancellation.
func (n *Notifier) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case e := <-n.queue:
			func() { c, cancel := context.WithTimeout(ctx, 30*time.Second); defer cancel(); n.send(c, e) }()
		}
	}
}

// Deliver sends the alert for result r of check c through c.Channel and
// returns when the transport has answered, with the delivery error if any.
// It is the synchronous seam under the queue: Run calls the same code, and
// tests and the channel-test feature call it directly. Failures are also
// logged, as the queue always did.
func (n *Notifier) Deliver(ctx context.Context, c config.Check, r check.Result) error {
	return n.deliver(ctx, event{check: c, result: r})
}

// DeliverCert is Deliver for a certificate expiry warning.
func (n *Notifier) DeliverCert(ctx context.Context, c config.Check, cert config.Cert, remaining time.Duration) error {
	return n.deliver(ctx, event{check: c, cert: &cert, remaining: remaining})
}

// send delivers a queued event; the outcome is only logged.
func (n *Notifier) send(ctx context.Context, e event) {
	_ = n.deliver(ctx, e)
}

// deliver looks up the event's channel and hands it to the transport for the
// channel's kind. Every transport logs its own failures and returns them.
func (n *Notifier) deliver(ctx context.Context, e event) error {
	c := e.check
	ch, ok, err := n.opts.Store.Channel(ctx, c.Channel)
	if err != nil {
		n.opts.Logger.Error("reading alert channel failed", "check", c.Name, "channel", c.Channel, "error", err)
		return fmt.Errorf("reading alert channel %q: %w", c.Channel, err)
	}
	if !ok {
		n.opts.Logger.Warn("alert channel not found", "check", c.Name, "channel", c.Channel)
		return fmt.Errorf("alert channel %q not found", c.Channel)
	}
	switch ch.Kind {
	case config.KindEmail:
		return n.deliverEmail(ctx, ch, e)
	case config.KindWebhook:
		return n.deliverWebhook(ctx, ch, e)
	}
	n.opts.Logger.Error("unsupported alert channel kind", "channel", c.Channel, "kind", ch.Kind)
	return fmt.Errorf("unsupported alert channel kind %q", ch.Kind)
}

func certUnit(n int, unit string) string {
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", n, unit)
}

func certMessage(c config.Check, cert config.Cert, remaining time.Duration) (string, string) {
	days := int(remaining / (24 * time.Hour))
	var left string
	if days >= 1 {
		left = certUnit(days, "day")
	} else {
		left = certUnit(max(int(remaining/time.Hour), 1), "hour")
	}
	next := 0
	for _, threshold := range config.CertWarnDays {
		if threshold < cert.Notified && threshold > next {
			next = threshold
		}
	}
	line := ""
	if next > 0 {
		line = fmt.Sprintf("  Next warning %d days before expiry\n", next)
	}
	subject := fmt.Sprintf("[up] %s: TLS certificate expires in %s", c.Name, left)
	body := fmt.Sprintf("The certificate served by %s expires in %s\n\n  Monitor   %s\n  Issuer    %s\n%s\nIf the certificate is renewed, up will detect it and stop warning.\n", c.Target(), left, c.Name, cert.Issuer, line)
	return subject, body
}
