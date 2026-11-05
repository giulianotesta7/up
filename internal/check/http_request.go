package check

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

// maxExpectRead bounds how much of a response is read to look for ExpectText.
const maxExpectRead = 1 << 20

// errUndecryptable is the only thing said about protected request values that
// cannot be opened; it never carries the values themselves.
var errUndecryptable = errors.New("cannot decrypt the request headers or body (encryption key missing or changed)")

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

// findText reports whether the response body contains text within the cap.
// truncated is true when the body is larger than the cap.
func findText(body io.Reader, text string) (found, truncated bool, err error) {
	data, err := io.ReadAll(io.LimitReader(body, maxExpectRead+1))
	if err != nil {
		return false, false, err
	}
	if len(data) > maxExpectRead {
		data, truncated = data[:maxExpectRead], true
	}
	return bytes.Contains(data, []byte(text)), truncated, nil
}

// textFailure is the reason ExpectText was not found.
func textFailure(truncated bool) string {
	if truncated {
		return "expected text not found in the first 1 MiB (the response is larger and was not read further)"
	}
	return "expected text not found"
}
