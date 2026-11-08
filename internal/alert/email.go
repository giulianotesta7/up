package alert

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
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
	e.trace.know(password)
	if password != "" {
		// The AUTH PLAIN response, which a server may echo encoded.
		e.trace.know("\x00" + server.Username + "\x00" + password)
	}
	msg := Message{From: server.Sender, To: ch.Recipients, Date: r.At}
	if e.test {
		msg = testMessage(ch, server.Sender, n.opts.Hostname, time.Now())
	} else {
		if e.cert != nil {
			msg.Date = e.cert.Observed
		}
		msg.Subject, msg.Body = n.content(ctx, e)
	}
	if err = n.opts.Sender.Send(ctx, server, password, msg); err != nil {
		n.opts.Logger.Error("sending alert failed", "check", c.Name, "channel", c.Channel, "error", err)
		return err
	}
	n.opts.Logger.Info("alert sent", "check", c.Name, "channel", c.Channel, "status", r.Status)
	return nil
}

// content is the subject and body of a down, recovery or certificate alert,
// the text every transport but webhook sends.
func (n *Notifier) content(ctx context.Context, e event) (subject, body string) {
	c, r := e.check, e.result
	at := r.At.In(n.opts.Location).Format("Jan 02 15:04:05 (MST)")
	switch {
	case e.cert != nil:
		return certMessage(c, *e.cert, e.remaining)
	case r.Status == check.StatusDown:
		if outages, err := n.opts.Store.Outages(ctx, c.Name, 1); err == nil && len(outages) > 0 && outages[0].Ongoing {
			at = outages[0].Start.In(n.opts.Location).Format("Jan 02 15:04:05 (MST)")
		}
		target := "URL:    " + c.Target()
		if c.Kind == config.CheckHeartbeat {
			target = heartbeatSchedule(c)
		}
		return fmt.Sprintf("[up] %s is DOWN", c.Name), fmt.Sprintf("%s is DOWN since %s.\n%s\nError:  %s\nHost:   %s\n", c.Name, at, target, r.Error, n.opts.Hostname)
	}
	subject = fmt.Sprintf("[up] %s is UP again", c.Name)
	first := fmt.Sprintf("%s is UP again at %s", c.Name, at)
	outages, err := n.opts.Store.Outages(ctx, c.Name, 1)
	if err == nil && len(outages) > 0 && !outages[0].Ongoing {
		first += fmt.Sprintf(", after %s down", outages[0].End.Sub(outages[0].Start).Round(time.Second))
	}
	if c.Kind == config.CheckHeartbeat {
		return subject, fmt.Sprintf("%s.\n%s\nHost:    %s\n", first, heartbeatSchedule(c), n.opts.Hostname)
	}
	return subject, fmt.Sprintf("%s.\nURL:     %s\nLatency: %s\nHost:    %s\n", first, c.Target(), r.Latency.Round(time.Millisecond), n.opts.Hostname)
}

// heartbeatSchedule stands in for the URL a heartbeat does not have: its
// period and grace ("Heartbeat: every 24h, grace 30m"). The ping token is
// never part of an alert.
func heartbeatSchedule(c config.Check) string {
	return fmt.Sprintf("Heartbeat: every %s, grace %s", shortDuration(c.Period), shortDuration(c.Grace))
}

// shortDuration drops zero minutes and seconds: "24h", "1h30m", "45m".
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
