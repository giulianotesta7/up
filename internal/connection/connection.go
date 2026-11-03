// Package connection holds what a client needs to describe a remote up server:
// URL rules and the failure kinds a connection check can report.
package connection

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Failure kinds a validator wraps so the TUI can explain them precisely.
var (
	ErrPlainHTTP     = errors.New("plain http is refused")
	ErrTokenRejected = errors.New("token rejected")
	ErrCertUntrusted = errors.New("certificate not trusted")
	ErrUnreachable   = errors.New("server unreachable")
)

// NormalizeURL returns the canonical https base URL of a server. Only https is
// accepted, so a token never travels in plain text. User info, query strings
// and fragments are rejected because they would leak or change the target.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("enter the server URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("enter a full URL such as https://up.example.com")
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		return "", ErrPlainHTTP
	default:
		return "", fmt.Errorf("unsupported scheme %q: use https://", u.Scheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", errors.New("the URL cannot contain credentials, a query or a fragment")
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}
