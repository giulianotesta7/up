package config

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/giulianotesta7/up/internal/secret"
)

// TLSMode is how the connection to an SMTP server is protected.
type TLSMode string

// TLS modes. StartTLS is the default.
const (
	TLSStartTLS TLSMode = "starttls"
	TLSImplicit TLSMode = "tls"
	TLSNone     TLSMode = "none"
)

// TLSModes lists the TLS modes in display order.
var TLSModes = []TLSMode{TLSStartTLS, TLSImplicit, TLSNone}

// KindEmail is the only channel kind so far.
const KindEmail = "email"

// ChannelKinds lists the channel kinds in display order.
var ChannelKinds = []string{KindEmail}

// SMTPServer is an SMTP server that email channels send through.
type SMTPServer struct {
	Name     string
	Host     string
	Port     int
	TLS      TLSMode
	Username string
	// Password is sealed to the daemon's public key; empty when unset.
	Password secret.Sealed
	// Sender is the From address, for example "up <alerts@example.com>".
	Sender string
}

// Channel is where a monitor's alerts go. An email channel sends through
// one SMTP server to one or more recipients.
type Channel struct {
	Name       string
	Kind       string
	SMTPServer string
	Recipients []string
}

// ValidateSMTPServer validates an SMTP server definition. A server without
// TLS cannot have a username, so a password never travels in plain text,
// and a password without a username is refused instead of being dropped.
func ValidateSMTPServer(s SMTPServer) error {
	var errs []error
	if s.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	switch {
	case s.Host == "":
		errs = append(errs, errors.New("host is required"))
	case strings.ContainsAny(s.Host, " /:\t"):
		errs = append(errs, fmt.Errorf("host %q must be a host name or IP address, without scheme or port", s.Host))
	}
	if s.Port < 1 || s.Port > 65535 {
		errs = append(errs, fmt.Errorf("port %d must be between 1 and 65535", s.Port))
	}
	switch s.TLS {
	case TLSStartTLS, TLSImplicit:
	case TLSNone:
		if s.Username != "" {
			errs = append(errs, errors.New(`TLS "none" cannot be used with a username (the password would travel in plain text)`))
		}
	default:
		errs = append(errs, fmt.Errorf("TLS %q must be starttls, tls or none", s.TLS))
	}
	if s.Username == "" && !s.Password.Empty() {
		errs = append(errs, errors.New("password requires a username"))
	}
	if s.Sender == "" {
		errs = append(errs, errors.New("sender is required"))
	} else if _, err := mail.ParseAddress(s.Sender); err != nil {
		errs = append(errs, fmt.Errorf("sender %q is not a valid email address", s.Sender))
	}
	return errors.Join(errs...)
}

// ValidateChannel validates a channel definition. Whether its SMTP server
// exists is checked by the store.
func ValidateChannel(c Channel) error {
	var errs []error
	if c.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if c.Kind != KindEmail {
		errs = append(errs, fmt.Errorf("kind %q is not supported (use email)", c.Kind))
	}
	if c.SMTPServer == "" {
		errs = append(errs, errors.New("SMTP server is required"))
	}
	if len(c.Recipients) == 0 {
		errs = append(errs, errors.New("at least one recipient is required"))
	}
	for _, r := range c.Recipients {
		if a, err := mail.ParseAddress(r); err != nil || a.Name != "" || a.Address != r {
			errs = append(errs, fmt.Errorf("recipient %q is not a valid email address", r))
		}
	}
	return errors.Join(errs...)
}

// ParseRecipients splits comma-separated addresses, trimming spaces and
// dropping empty entries.
func ParseRecipients(text string) []string {
	var out []string
	for _, part := range strings.Split(text, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
