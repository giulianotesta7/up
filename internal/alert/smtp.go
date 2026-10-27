// Package alert sends monitor alerts through the channels configured in the database.
package alert

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

// SMTPSender delivers messages over SMTP.
type SMTPSender struct {
	// Dial opens the TCP connection; nil uses a net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// TLSConfig is cloned for each connection with ServerName set to the
	// server host; nil uses the system roots.
	TLSConfig *tls.Config
	// Timeout bounds one whole delivery; zero means 30s.
	Timeout time.Duration
}

// Send delivers msg through server, authenticating with password when the
// server has a username.
func (s SMTPSender) Send(ctx context.Context, server config.SMTPServer, password string, msg Message) (err error) {
	addr := net.JoinHostPort(server.Host, strconv.Itoa(server.Port))
	defer func() {
		if err != nil {
			err = fmt.Errorf("alert: send via %s: %w", addr, err)
		}
	}()
	if server.TLS == config.TLSNone && server.Username != "" {
		return fmt.Errorf("authentication without TLS is forbidden")
	}
	switch server.TLS {
	case "", config.TLSStartTLS, config.TLSImplicit, config.TLSNone:
	default:
		return fmt.Errorf("unknown TLS mode %q", server.TLS)
	}
	timeout := s.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	dial := s.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, e := dial(ctx, "tcp", addr)
	if e != nil {
		return e
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if e = conn.SetDeadline(deadline); e != nil {
		return e
	}
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stop()
	tlsConfig := &tls.Config{}
	if s.TLSConfig != nil {
		tlsConfig = s.TLSConfig.Clone()
	}
	tlsConfig.ServerName = server.Host
	if server.TLS == config.TLSImplicit {
		tc := tls.Client(conn, tlsConfig)
		if e = tc.HandshakeContext(ctx); e != nil {
			return e
		}
		conn = tc
	}
	c, e := smtp.NewClient(conn, server.Host)
	if e != nil {
		return e
	}
	defer c.Close()
	if e = c.Hello("localhost"); e != nil {
		return e
	}
	if server.TLS == "" || server.TLS == config.TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("server does not offer STARTTLS")
		}
		if e = c.StartTLS(tlsConfig); e != nil {
			return e
		}
	}
	if server.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return fmt.Errorf("server does not offer AUTH")
		}
		if e = c.Auth(smtp.PlainAuth("", server.Username, password, server.Host)); e != nil {
			return e
		}
	}
	from, e := mail.ParseAddress(msg.From)
	if e != nil {
		return e
	}
	data, e := msg.Bytes()
	if e != nil {
		return e
	}
	if e = c.Mail(from.Address); e != nil {
		return e
	}
	for _, to := range msg.To {
		if e = c.Rcpt(to); e != nil {
			return e
		}
	}
	w, e := c.Data()
	if e != nil {
		return e
	}
	if _, e = w.Write(data); e != nil {
		return e
	}
	if e = w.Close(); e != nil {
		return e
	}
	return c.Quit()
}
