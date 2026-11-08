package check

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
)

// maxExpectRead bounds how much of a response is read to look for ExpectText
// and to evaluate JSON and body regex conditions.
const maxExpectRead = 1 << 20

// errUndecryptable is the only thing said about protected request values that
// cannot be opened; it never carries the values themselves.
var errUndecryptable = errors.New("cannot decrypt the request headers or body (encryption key missing or changed)")

// errRedirectLeavesOrigin is why a check that carries protected values stops
// at a redirect to another origin. Go forwards custom headers (and, on 307 and
// 308, the body) across hosts, so such a redirect is never followed.
var errRedirectLeavesOrigin = errors.New("redirect to another host not followed: the request carries protected values")

// client returns the HTTP client for c. A check with headers or a body gets a
// copy whose CheckRedirect refuses to leave the original scheme, host and
// port; the shared client is never modified.
func (ch *Checker) client(c config.Check) *http.Client {
	if len(c.Headers) == 0 && c.Body.Empty() {
		return ch.Client
	}
	cl := *ch.Client
	inner := cl.CheckRedirect
	cl.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if origin(req.URL) != origin(via[0].URL) {
			return errRedirectLeavesOrigin
		}
		if inner != nil {
			return inner(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &cl
}

// origin is scheme, host and port, with the default port made explicit.
func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// newRequest builds the request for c: its method (empty is GET), the sealed
// headers and body opened with ch.Key, and the default user agent unless a
// header replaces it.
func (ch *Checker) newRequest(ctx context.Context, c config.Check) (*http.Request, error) {
	method := c.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if !c.Body.Empty() {
		text, err := ch.open(c.Body)
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(text)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	replaced := map[string]bool{}
	for _, h := range c.Headers {
		value, err := ch.open(h.Value)
		if err != nil {
			return nil, err
		}
		name := http.CanonicalHeaderKey(h.Name)
		switch {
		case name == "Host":
			req.Host = value
		case replaced[name]:
			req.Header.Add(name, value)
		default:
			req.Header.Set(name, value)
			replaced[name] = true
		}
	}
	return req, nil
}

// open decrypts one protected value; an empty value is the empty string.
func (ch *Checker) open(v secret.Sealed) (string, error) {
	if v.Empty() {
		return "", nil
	}
	if ch.Key == nil {
		return "", errUndecryptable
	}
	text, err := ch.Key.Open(v)
	if err != nil {
		return "", errUndecryptable
	}
	return text, nil
}

// readBody reads the response body up to maxExpectRead bytes. truncated is
// true when the body is larger than the cap.
func readBody(body io.Reader) (data []byte, truncated bool, err error) {
	data, err = io.ReadAll(io.LimitReader(body, maxExpectRead+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxExpectRead {
		data, truncated = data[:maxExpectRead], true
	}
	return data, truncated, nil
}

// textFailure is the reason ExpectText was not found.
func textFailure(truncated bool) string {
	if truncated {
		return "expected text not found in the first 1 MiB (the response is larger and was not read further)"
	}
	return "expected text not found"
}
