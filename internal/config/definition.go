package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/heohlee/up/internal/secret"
)

// Check kinds; CheckHeartbeat is in heartbeat.go.
const (
	CheckHTTP = "http"
	CheckTCP  = "tcp"
)

// HTTPMethods lists the methods an HTTP check may use, in display order.
var HTTPMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// Bounds on the HTTP request fields.
const (
	MaxHeaders = 32
	// MaxHeaderName is the longest header name in bytes.
	MaxHeaderName = 128
	// MaxSealedHeaderValue is the largest sealed header value in bytes
	// (4000 bytes of plain text plus the sealing overhead).
	MaxSealedHeaderValue = 4096
	// MaxSealedBody is the largest sealed request body in bytes (64 KiB of
	// plain text plus the sealing overhead).
	MaxSealedBody = 64<<10 + 256
	// MaxExpectText is the longest expected response text in bytes.
	MaxExpectText = 1024
)

// Edit says what a write does to a protected value. The zero value keeps.
//
// Protected values are sealed to the daemon's public key exactly like SMTP
// passwords (see package secret) and are never returned by the snapshot or
// the API, which only report whether one is set. The same three actions
// apply to every protected value: header values, the HTTP body, the webhook
// URL and the webhook auth value.
type Edit int

// Edits.
const (
	// EditKeep keeps the stored value on update. On create it stores the
	// accompanying Sealed value as given (empty when none), so a value read
	// from the store can be written back unchanged.
	EditKeep Edit = iota
	// EditReplace stores the accompanying Sealed value, which must not be empty.
	EditReplace
	// EditClear stores no value.
	EditClear
)

// String returns the name used on the wire and in messages.
func (e Edit) String() string {
	switch e {
	case EditKeep:
		return "keep"
	case EditReplace:
		return "replace"
	case EditClear:
		return "clear"
	}
	return fmt.Sprintf("Edit(%d)", int(e))
}

// ParseEdit converts the wire form; an empty name means keep.
func ParseEdit(s string) (Edit, error) {
	switch s {
	case "", "keep":
		return EditKeep, nil
	case "replace":
		return EditReplace, nil
	case "clear":
		return EditClear, nil
	}
	return EditKeep, fmt.Errorf("edit %q must be keep, replace or clear", s)
}

// Header is one request header. Name is plain and Value is sealed. On
// update, EditKeep keeps the stored value of the header with the same name
// (compared without regard to case; a repeated name matches in order), so a
// header absent from the list is removed.
type Header struct {
	Name  string
	Value secret.Sealed
	Edit  Edit
}

// Canonical returns c in its one stored form, without changing c:
// kind "http" becomes "", method "GET" becomes "", schedule "period"
// becomes "", the cron fields are separated by one space, an empty header or
// condition list becomes nil. Edit intents are kept. Copies the header and
// condition slices.
func (c Check) Canonical() Check {
	if c.Kind == CheckHTTP {
		c.Kind = ""
	}
	if c.Method == "GET" {
		c.Method = ""
	}
	if c.Schedule == SchedulePeriod {
		c.Schedule = ""
	}
	c.Cron = strings.Join(strings.Fields(c.Cron), " ")
	if len(c.Headers) == 0 {
		c.Headers = nil
	} else {
		c.Headers = slices.Clone(c.Headers)
	}
	if len(c.Conditions) == 0 {
		c.Conditions = nil
	} else {
		c.Conditions = slices.Clone(c.Conditions)
	}
	return c
}

// SameDefinition reports whether two checks are the same definition: every
// field the daemon uses to run or alert is equal once canonical, including
// the stored (sealed) values. Edit intents are not part of identity. The
// monitor restarts a loop, and the store drops an in-flight result, when this
// is false, so a new field that changes how a check runs must be added here
// and to store.RecordDefined.
func (c Check) SameDefinition(o Check) bool {
	a, b := c.Canonical(), o.Canonical()
	return a.Name == b.Name && a.Kind == b.Kind && a.URL == b.URL &&
		a.Interval == b.Interval && a.Timeout == b.Timeout && a.ExpectedStatus == b.ExpectedStatus &&
		a.Channel == b.Channel && a.AlertAfter == b.AlertAfter && a.RecoverAfter == b.RecoverAfter && a.TLSOff == b.TLSOff &&
		a.Method == b.Method && sameSealed(a.Body, b.Body) && a.ExpectText == b.ExpectText &&
		a.Host == b.Host && a.Port == b.Port && a.Period == b.Period && a.Grace == b.Grace &&
		a.Schedule == b.Schedule && a.Cron == b.Cron &&
		slices.Equal(a.Conditions, b.Conditions) &&
		slices.EqualFunc(a.Headers, b.Headers, func(x, y Header) bool {
			return x.Name == y.Name && sameSealed(x.Value, y.Value)
		})
}

func sameSealed(a, b secret.Sealed) bool {
	return bytes.Equal(a.Key, b.Key) && bytes.Equal(a.Data, b.Data)
}

// validateEdit checks an edit intent against its sealed value. what names
// the value in messages, for example `header "X"` or "body".
func validateEdit(what string, e Edit, v secret.Sealed, max int) []error {
	var errs []error
	switch e {
	case EditKeep, EditClear:
	case EditReplace:
		if v.Empty() {
			errs = append(errs, fmt.Errorf("%s replace needs a value", what))
		}
	default:
		errs = append(errs, fmt.Errorf("%s has an unknown edit", what))
	}
	if len(v.Data) > max {
		errs = append(errs, fmt.Errorf("%s is too large", what))
	}
	return errs
}

// ValidHeaderName reports whether s is an HTTP header field name (an RFC
// 9110 token).
func ValidHeaderName(s string) bool {
	if s == "" || len(s) > MaxHeaderName {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
		if !ok {
			return false
		}
	}
	return true
}

// validateHTTPFields checks the HTTP-only request fields and that no TCP
// field is set. Bounds: at most MaxHeaders headers, names are tokens,
// sealed values and the expected text stay within the Max constants.
// Conditions are checked by validateConditions.
func validateHTTPFields(c Check) []error {
	var errs []error
	if c.Method != "" && !slices.Contains(HTTPMethods, c.Method) {
		errs = append(errs, fmt.Errorf("method %q must be one of %s", c.Method, strings.Join(HTTPMethods, ", ")))
	}
	if len(c.Headers) > MaxHeaders {
		errs = append(errs, fmt.Errorf("at most %d headers are allowed", MaxHeaders))
	}
	seen := map[string]bool{}
	for _, h := range c.Headers {
		if key := strings.ToLower(h.Name); h.Name != "" && seen[key] {
			errs = append(errs, fmt.Errorf("header %q is repeated (names are compared without regard to case)", h.Name))
		} else {
			seen[key] = true
		}
		switch {
		case h.Name == "":
			errs = append(errs, errors.New("header name is required"))
		case !ValidHeaderName(h.Name):
			errs = append(errs, fmt.Errorf("header name %q is not valid", h.Name))
		}
		errs = append(errs, validateEdit(fmt.Sprintf("header %q", h.Name), h.Edit, h.Value, MaxSealedHeaderValue)...)
	}
	errs = append(errs, validateEdit("body", c.BodyEdit, c.Body, MaxSealedBody)...)
	if len(c.ExpectText) > MaxExpectText {
		errs = append(errs, fmt.Errorf("expected text must be at most %d bytes", MaxExpectText))
	}
	if c.Method == "HEAD" && c.ExpectText != "" {
		errs = append(errs, errors.New("expected text needs a method that returns a body; HEAD does not"))
	}
	errs = append(errs, validateConditions(c.Conditions, c.Timeout)...)
	if c.Method == "HEAD" && slices.ContainsFunc(c.Conditions, readsBody) {
		errs = append(errs, errors.New("JSON and body regex conditions need a method that returns a body; HEAD does not"))
	}
	if c.Host != "" || c.Port != 0 {
		errs = append(errs, errors.New("host and port are only for tcp checks"))
	}
	return errs
}

// validateTCPFields checks the target and that no HTTP-only field is set.
// Host is a name or IP without scheme, brackets or port; an IPv6 literal is
// written bare ("2001:db8::1"). Port is 1..65535.
func validateTCPFields(c Check) []error {
	var errs []error
	switch {
	case c.Host == "":
		errs = append(errs, errors.New("host is required"))
	case strings.ContainsAny(c.Host, " \t/@[]"):
		errs = append(errs, fmt.Errorf("host %q must be a host name or IP address, without scheme, brackets or port", c.Host))
	case strings.Contains(c.Host, ":"):
		if _, err := netip.ParseAddr(c.Host); err != nil {
			errs = append(errs, fmt.Errorf("host %q must be a host name or IP address, without scheme, brackets or port", c.Host))
		}
	}
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, fmt.Errorf("port %d must be between 1 and 65535", c.Port))
	}
	if c.URL != "" {
		errs = append(errs, errors.New("url is only for http checks"))
	}
	if c.ExpectedStatus != 0 {
		errs = append(errs, errors.New("expected status is only for http checks"))
	}
	if c.Method != "" || len(c.Headers) > 0 || !c.Body.Empty() || c.BodyEdit != EditKeep || c.ExpectText != "" {
		errs = append(errs, errors.New("method, headers, body and expected text are only for http checks"))
	}
	if len(c.Conditions) > 0 {
		errs = append(errs, errors.New("conditions are only for http checks"))
	}
	return errs
}
