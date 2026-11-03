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
	ErrTokenRejected = errors.New("token rejected")
	ErrCertUntrusted = errors.New("certificate not trusted")
	ErrUnreachable   = errors.New("server unreachable")
)

// NormalizeURL returns the canonical base URL of a server. https and http are
// accepted; plain http is the user's choice and the UI warns about it (see
// IsPlainHTTP). User info, query strings and fragments are rejected because
// they would leak or change the target.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("enter the server URL")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("enter a full URL such as https://up.example.com")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", fmt.Errorf("unsupported scheme %q: use https:// or http://", u.Scheme)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", errors.New("the URL cannot contain credentials, a query or a fragment")
	}
	u.Scheme = scheme
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

// IsPlainHTTP reports whether raw starts with http://, where the token travels
// unencrypted.
func IsPlainHTTP(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "http://")
}
