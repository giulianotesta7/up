package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Push channel defaults. An empty Priority means the kind's default.
const (
	DefaultNtfyServer     = "https://ntfy.sh"
	DefaultNtfyPriority   = "default"
	DefaultGotifyPriority = "5"
	// MaxGotifyPriority is the highest gotify priority; the lowest is 0.
	MaxGotifyPriority = 10
)

// NtfyPriorities lists the ntfy priorities, lowest first.
var NtfyPriorities = []string{"min", "low", DefaultNtfyPriority, "high", "max"}

var (
	ntfyTopic      = regexp.MustCompile(`^[-_A-Za-z0-9]{1,64}$`)
	telegramChat   = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z][A-Za-z0-9_]{3,31})$`)
	telegramToken  = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)
	errServerURL   = errors.New("server URL must be http or https with a host and no credentials, query or fragment")
	tokenKinds     = []string{KindNtfy, KindTelegram, KindGotify}
	webhookURLKind = []string{KindWebhook, KindDiscord, KindSlack}
)

// ServerURL returns the stored form of an ntfy or gotify server URL: trimmed,
// without trailing slashes, with the scheme and host lower-cased and the path
// kept. Plain http is accepted. Its errors never echo the text, which may
// carry credentials.
func ServerURL(raw string) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" || strings.ContainsAny(s, "?# \t") {
		return "", errServerURL
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + u.EscapedPath(), nil
}

// ValidTelegramToken reports whether s looks like a bot token
// ("123456:ABC-def_1"). It goes in the request path, so nothing else may.
func ValidTelegramToken(s string) bool { return telegramToken.MatchString(s) }

// SendsTo describes where a channel delivers without any secret, for the
// channel list: "gmail → ana@acme.com +1", an origin, "https://ntfy.sh ·
// up-alerts", "chat -1001234567890". It is empty for an unknown kind.
func (c Channel) SendsTo() string {
	switch c.Kind {
	case KindEmail:
		s := c.SMTPServer
		if len(c.Recipients) > 0 {
			s += " → " + c.Recipients[0]
		}
		if len(c.Recipients) > 1 {
			s += fmt.Sprintf(" +%d", len(c.Recipients)-1)
		}
		return s
	case KindWebhook, KindDiscord, KindSlack:
		return c.WebhookOrigin
	case KindNtfy:
		return c.ServerURL + " · " + c.Topic
	case KindTelegram:
		return "chat " + c.ChatID
	case KindGotify:
		return c.ServerURL
	}
	return ""
}

// validatePushChannel checks an ntfy, telegram, discord, slack or gotify
// channel. Whether a kept token or URL has a stored value behind it is
// checked by the store.
func validatePushChannel(c Channel) []error {
	var errs []error
	switch c.Kind {
	case KindNtfy:
		errs = append(errs, validateServerURL(c)...)
		switch {
		case c.Topic == "":
			errs = append(errs, errors.New("ntfy topic is required"))
		case !ntfyTopic.MatchString(c.Topic):
			errs = append(errs, fmt.Errorf("ntfy topic %q may only use letters, digits, - and _ (at most 64)", c.Topic))
		}
		if c.Priority != "" && !slices.Contains(NtfyPriorities, c.Priority) {
			errs = append(errs, fmt.Errorf("ntfy priority %q must be min, low, default, high or max", c.Priority))
		}
		errs = append(errs, validateEdit("ntfy access token", c.TokenEdit, c.Token, MaxSealedHeaderValue)...)
	case KindTelegram:
		errs = append(errs, validateToken("telegram bot token", c)...)
		switch {
		case c.ChatID == "":
			errs = append(errs, errors.New("telegram chat ID is required"))
		case !telegramChat.MatchString(c.ChatID):
			errs = append(errs, fmt.Errorf("telegram chat ID %q must be a number like -1001234567890 or @name", c.ChatID))
		}
	case KindDiscord, KindSlack:
		errs = append(errs, validateWebhookURL(c.Kind+" webhook", c)...)
	case KindGotify:
		errs = append(errs, validateServerURL(c)...)
		errs = append(errs, validateToken("gotify app token", c)...)
		if c.Priority != "" {
			if n, err := strconv.Atoi(c.Priority); err != nil || n < 0 || n > MaxGotifyPriority || strconv.Itoa(n) != c.Priority {
				errs = append(errs, fmt.Errorf("gotify priority %q must be a whole number from 0 to %d", c.Priority, MaxGotifyPriority))
			}
		}
	}
	if c.SMTPServer != "" || len(c.Recipients) > 0 {
		errs = append(errs, errors.New("SMTP server and recipients are only for email channels"))
	}
	if !slices.Contains(webhookURLKind, c.Kind) && (c.WebhookOrigin != "" || !c.WebhookURL.Empty() || c.WebhookURLEdit != EditKeep) {
		errs = append(errs, errors.New("webhook URL is only for webhook, discord and slack channels"))
	}
	if c.WebhookAuthName != "" || !c.WebhookAuth.Empty() || c.WebhookAuthEdit != EditKeep {
		errs = append(errs, errors.New("webhook auth header is only for webhook channels"))
	}
	return append(errs, pushFieldErrors(c)...)
}

func validateServerURL(c Channel) []error {
	if c.ServerURL == "" {
		return []error{fmt.Errorf("%s server URL is required", c.Kind)}
	}
	if s, err := ServerURL(c.ServerURL); err != nil || s != c.ServerURL {
		return []error{fmt.Errorf("%s %w", c.Kind, errServerURL)}
	}
	return nil
}

// validateToken checks a required token: telegram's bot token and gotify's
// app token cannot be cleared.
func validateToken(what string, c Channel) []error {
	errs := validateEdit(what, c.TokenEdit, c.Token, MaxSealedHeaderValue)
	if c.TokenEdit == EditClear {
		errs = append(errs, fmt.Errorf("%s cannot be cleared", what))
	}
	return errs
}

// pushFieldErrors reports the push fields set on a kind that does not use
// them.
func pushFieldErrors(c Channel) []error {
	var errs []error
	uses := func(kinds ...string) bool { return slices.Contains(kinds, c.Kind) }
	if c.ServerURL != "" && !uses(KindNtfy, KindGotify) {
		errs = append(errs, errors.New("server URL is only for ntfy and gotify channels"))
	}
	if c.Topic != "" && !uses(KindNtfy) {
		errs = append(errs, errors.New("topic is only for ntfy channels"))
	}
	if c.ChatID != "" && !uses(KindTelegram) {
		errs = append(errs, errors.New("chat ID is only for telegram channels"))
	}
	if (!c.Token.Empty() || c.TokenEdit != EditKeep) && !uses(tokenKinds...) {
		errs = append(errs, errors.New("token is only for ntfy, telegram and gotify channels"))
	}
	if c.Priority != "" && !uses(KindNtfy, KindGotify) {
		errs = append(errs, errors.New("priority is only for ntfy and gotify channels"))
	}
	return errs
}
