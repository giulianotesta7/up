package alert

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

// deliverEmail renders the event as an email and sends it through the
// channel's SMTP server. It returns every failure; errors carry no secret.
func (n *Notifier) deliverEmail(ctx context.Context, ch config.Channel, e event) error {
	c, r := e.check, e.result
	server, ok, err := n.opts.Store.SMTPServer(ctx, ch.SMTPServer)
	if err != nil {
		n.opts.Logger.Error("reading SMTP server failed", "server", ch.SMTPServer, "error", err)
		return fmt.Errorf("reading SMTP server %q: %w", ch.SMTPServer, err)
	}
	if !ok {
		n.opts.Logger.Error("SMTP server not found", "server", ch.SMTPServer)
		return fmt.Errorf("SMTP server %q not found", ch.SMTPServer)
	}
	password := ""
	if !server.Password.Empty() {
		if n.opts.Key == nil {
			n.opts.Logger.Error("SMTP server password cannot be opened", "server", ch.SMTPServer)
			return fmt.Errorf("SMTP server %q password cannot be opened: no encryption key", ch.SMTPServer)
		}
		password, err = n.opts.Key.Open(server.Password)
		if errors.Is(err, secret.ErrWrongKey) {
			n.opts.Logger.Error("SMTP server password was sealed to another key; enter it again in the TUI", "server", ch.SMTPServer)
			return fmt.Errorf("SMTP server %q password was sealed to another key; enter it again", ch.SMTPServer)
		}
		if err != nil {
			n.opts.Logger.Error("opening SMTP server password failed", "server", ch.SMTPServer, "error", err)
			return fmt.Errorf("opening SMTP server %q password failed", ch.SMTPServer)
		}
	}
	at := r.At.In(n.opts.Location).Format("Jan 02 15:04:05 (MST)")
	msg := Message{From: server.Sender, To: ch.Recipients, Date: r.At}
	if e.test {
		msg = testMessage(ch, server.Sender, n.opts.Hostname, time.Now())
	} else if e.cert != nil {
		msg.Date = e.cert.Observed
		msg.Subject, msg.Body = certMessage(c, *e.cert, e.remaining)
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
		return err
	}
	n.opts.Logger.Info("alert sent", "check", c.Name, "channel", c.Channel, "status", r.Status)
	return nil
}
