package alert

import (
	"context"
	"errors"
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

func (n *Notifier) send(ctx context.Context, e event) {
	c, r := e.check, e.result
	ch, ok, err := n.opts.Store.Channel(ctx, c.Channel)
	if err != nil {
		n.opts.Logger.Error("reading alert channel failed", "check", c.Name, "channel", c.Channel, "error", err)
		return
	}
	if !ok {
		n.opts.Logger.Warn("alert channel not found", "check", c.Name, "channel", c.Channel)
		return
	}
	if ch.Kind != config.KindEmail {
		n.opts.Logger.Error("unsupported alert channel kind", "channel", c.Channel, "kind", ch.Kind)
		return
	}
	server, ok, err := n.opts.Store.SMTPServer(ctx, ch.SMTPServer)
	if err != nil {
		n.opts.Logger.Error("reading SMTP server failed", "server", ch.SMTPServer, "error", err)
		return
	}
	if !ok {
		n.opts.Logger.Error("SMTP server not found", "server", ch.SMTPServer)
		return
	}
	password := ""
	if !server.Password.Empty() {
		if n.opts.Key == nil {
			n.opts.Logger.Error("SMTP server password cannot be opened", "server", ch.SMTPServer)
			return
		}
		password, err = n.opts.Key.Open(server.Password)
		if errors.Is(err, secret.ErrWrongKey) {
			n.opts.Logger.Error("SMTP server password was sealed to another key; enter it again in the TUI", "server", ch.SMTPServer)
			return
		}
		if err != nil {
			n.opts.Logger.Error("opening SMTP server password failed", "server", ch.SMTPServer, "error", err)
			return
		}
	}
	at := r.At.In(n.opts.Location).Format("Jan 02 15:04:05 (MST)")
	msg := Message{From: server.Sender, To: ch.Recipients, Date: r.At}
	if e.cert != nil {
		msg.Date = e.cert.Observed
		msg.Subject, msg.Body = certMessage(c, *e.cert, e.remaining, n.opts.Location)
	} else if r.Status == check.StatusDown {
		if outages, err := n.opts.Store.Outages(ctx, c.Name, 1); err == nil && len(outages) > 0 && outages[0].Ongoing {
			at = outages[0].Start.In(n.opts.Location).Format("Jan 02 15:04:05 (MST)")
		}
		msg.Subject = fmt.Sprintf("[up] %s is DOWN", c.Name)
		msg.Body = fmt.Sprintf("%s is DOWN since %s.\nURL:    %s\nError:  %s\nHost:   %s\n", c.Name, at, c.URL, r.Error, n.opts.Hostname)
	} else {
		msg.Subject = fmt.Sprintf("[up] %s is UP again", c.Name)
		first := fmt.Sprintf("%s is UP again at %s", c.Name, at)
		outages, err := n.opts.Store.Outages(ctx, c.Name, 1)
		if err == nil && len(outages) > 0 && !outages[0].Ongoing {
			first += fmt.Sprintf(", after %s down", outages[0].End.Sub(outages[0].Start).Round(time.Second))
		}
		msg.Body = fmt.Sprintf("%s.\nURL:     %s\nLatency: %s\nHost:    %s\n", first, c.URL, r.Latency.Round(time.Millisecond), n.opts.Hostname)
	}
	if err = n.opts.Sender.Send(ctx, server, password, msg); err != nil {
		n.opts.Logger.Error("sending alert failed", "check", c.Name, "channel", c.Channel, "error", err)
		return
	}
	n.opts.Logger.Info("alert sent", "check", c.Name, "channel", c.Channel, "status", r.Status)
}

func certUnit(n int, unit string) string {
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s", n, unit)
}

func certMessage(c config.Check, cert config.Cert, remaining time.Duration, loc *time.Location) (string, string) {
	days := int(remaining / (24 * time.Hour))
	hours := int(remaining / time.Hour)
	var subjectDuration string
	if days >= 1 {
		subjectDuration = certUnit(days, "day")
	} else {
		subjectDuration = certUnit(max(hours, 1), "hour")
	}
	var duration string
	switch {
	case days >= 1:
		duration = certUnit(days, "day") + " " + certUnit(hours%24, "hour")
	case hours >= 1:
		duration = certUnit(hours, "hour")
	default:
		duration = certUnit(max(int(remaining/time.Minute), 0), "minute")
	}
	next := 0
	for _, threshold := range config.CertWarnDays {
		if threshold < cert.Notified && threshold > next {
			next = threshold
		}
	}
	line := ""
	if next > 0 {
		line = fmt.Sprintf("  Next warning at %d days.\n", next)
	}
	subject := fmt.Sprintf("[up] %s: TLS certificate expires in %s", c.Name, subjectDuration)
	body := fmt.Sprintf("The certificate served by %s expires on %s (%s from now).\n\n  Monitor   %s (%s)\n  Issuer    %s\n%s\nIf the certificate is renewed automatically, up will\ndetect the new one and stop warning.\n", cert.Host, cert.NotAfter.UTC().Format("2006-01-02 15:04 UTC"), duration, c.Name, c.URL, cert.Issuer, line)
	return subject, body
}
