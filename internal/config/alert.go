package config

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"

	"github.com/heohlee/up/internal/secret"
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

// Channel kinds.
const (
	KindEmail    = "email"
	KindWebhook  = "webhook"
	KindNtfy     = "ntfy"
	KindTelegram = "telegram"
	KindDiscord  = "discord"
	KindSlack    = "slack"
	KindGotify   = "gotify"
)

// ChannelKinds lists the kinds the TUI offers, in display order.
var ChannelKinds = []string{KindEmail, KindWebhook, KindNtfy, KindTelegram, KindDiscord, KindSlack, KindGotify}

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
// one SMTP server to one or more recipients. A webhook channel posts to one
// URL. Discord and slack channels post to their webhook URL, ntfy to a topic
// on a server, telegram to a chat through a bot and gotify to a server's
// application.
//
// Webhook fields follow the protected-value rules of Edit: WebhookURL and
// WebhookAuth are sealed and never leave the store in snapshots or the API.
// WebhookOrigin ("scheme://host[:port]", see WebhookOrigin) is the only part
// of the URL shown; whoever seals the URL derives it from the same text.
// WebhookAuthName is the plain header name ("Authorization"); the optional
// WebhookAuth value is sent in that header. Discord and slack use WebhookURL
// and WebhookOrigin the same way, without the auth header.
//
// ServerURL (ntfy, gotify; see ServerURL) and Topic (ntfy), ChatID
// (telegram) and Priority (ntfy: one of NtfyPriorities; gotify: "0" to "10";
// empty is the kind's default) are plain. Token is the ntfy access token
// (optional), the telegram bot token or the gotify app token, sealed and
// edited like WebhookURL; only the ntfy access token can be cleared.
type Channel struct {
	Name       string
	Kind       string
	SMTPServer string
	Recipients []string

	WebhookOrigin   string
	WebhookURL      secret.Sealed
	WebhookURLEdit  Edit
	WebhookAuthName string
	WebhookAuth     secret.Sealed
	WebhookAuthEdit Edit

	ServerURL string
	Topic     string
	ChatID    string
	Token     secret.Sealed
	TokenEdit Edit
	Priority  string
}

// WebhookOrigin returns the scheme and host (with port, if any) of a webhook
// URL, lower-cased, for display. It never includes the path, query or
// credentials, and its errors never echo credentials.
func WebhookOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", errors.New("webhook URL must be http or https with a host and no credentials")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

func validWebhookOrigin(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil &&
		(u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == "" && !strings.HasSuffix(s, "/")
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
	switch c.Name {
	case "":
		errs = append(errs, errors.New("name is required"))
	case "none":
		// The monitor form offers "none" for no channel.
		errs = append(errs, errors.New(`name "none" is reserved (it means no channel)`))
	}
	switch c.Kind {
	case KindEmail:
		errs = append(errs, validateEmailChannel(c)...)
	case KindWebhook:
		errs = append(errs, validateWebhookChannel(c)...)
	case KindNtfy, KindTelegram, KindDiscord, KindSlack, KindGotify:
		errs = append(errs, validatePushChannel(c)...)
	default:
		errs = append(errs, fmt.Errorf("kind %q is not supported (use email, webhook, ntfy, telegram, discord, slack or gotify)", c.Kind))
	}
	return errors.Join(errs...)
}

func validateEmailChannel(c Channel) []error {
	var errs []error
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
	if c.WebhookOrigin != "" || !c.WebhookURL.Empty() || c.WebhookURLEdit != EditKeep || c.WebhookAuthName != "" || !c.WebhookAuth.Empty() || c.WebhookAuthEdit != EditKeep {
		errs = append(errs, errors.New("webhook fields are only for webhook channels"))
	}
	return append(errs, pushFieldErrors(c)...)
}

// validateWebhookChannel checks a webhook channel. The URL cannot be cleared
// (a webhook without one is meaningless); whether a "keep" has a stored URL
// behind it is checked by the store.
func validateWebhookChannel(c Channel) []error {
	errs := validateWebhookURL("webhook", c)
	if c.WebhookAuthName != "" && !ValidHeaderName(c.WebhookAuthName) {
		errs = append(errs, fmt.Errorf("webhook auth header name %q is not valid", c.WebhookAuthName))
	}
	errs = append(errs, validateEdit("webhook auth value", c.WebhookAuthEdit, c.WebhookAuth, MaxSealedHeaderValue)...)
	if c.WebhookAuthName == "" && !c.WebhookAuth.Empty() {
		errs = append(errs, errors.New("webhook auth value requires a header name"))
	}
	if c.SMTPServer != "" || len(c.Recipients) > 0 {
		errs = append(errs, errors.New("SMTP server and recipients are only for email channels"))
	}
	return append(errs, pushFieldErrors(c)...)
}

// validateWebhookURL checks the origin and the sealed URL of a webhook,
// discord or slack channel; what names them in messages ("webhook",
// "discord webhook").
func validateWebhookURL(what string, c Channel) []error {
	var errs []error
	switch {
	case c.WebhookOrigin == "":
		errs = append(errs, fmt.Errorf("%s origin is required", what))
	case !validWebhookOrigin(c.WebhookOrigin):
		errs = append(errs, fmt.Errorf("%s origin %q must be http or https with a host", what, c.WebhookOrigin))
	}
	errs = append(errs, validateEdit(what+" URL", c.WebhookURLEdit, c.WebhookURL, MaxSealedHeaderValue)...)
	if c.WebhookURLEdit == EditClear {
		errs = append(errs, fmt.Errorf("%s URL cannot be cleared", what))
	}
	return errs
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
