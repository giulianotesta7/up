package config

import (
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"slices"
	"strings"
	"time"
)

// Condition kinds, in the order the form offers them.
const (
	// ConditionJSON: the JSON value at Path equals Value.
	ConditionJSON = "json"
	// ConditionRegex: the response body matches Pattern.
	ConditionRegex = "regex"
	// ConditionResponseTime: the response arrives within Within.
	ConditionResponseTime = "response_time"
	// ConditionHeader: the response header Name equals Value.
	ConditionHeader = "header"
)

// ConditionKinds lists the condition kinds in display order.
var ConditionKinds = []string{ConditionJSON, ConditionRegex, ConditionResponseTime, ConditionHeader}

// Bounds on conditions.
const (
	MaxConditions = 32
	// MaxConditionText is the longest path, value or pattern in bytes.
	MaxConditionText = 1024
)

// Condition is one extra rule an HTTP response must meet once its status is
// the expected one. All of a check's conditions must pass, in order; the first
// that fails is the reason the check is down. Each kind uses only its fields
// (see the kind constants); Canonical drops the others.
//
// Comparisons ignore case: header names and values and JSON values. A JSON
// value is compared as text: a string without quotes, any other value as its
// JSON text, so "true" matches true and "true", and "3" matches 3. An empty
// Value only requires the JSON path or header to be present. A pattern
// follows Go's regexp syntax and its own flags, such as (?i).
type Condition struct {
	Kind string
	// Path is the dot-separated JSON path; numbers index arrays
	// (services.0.status). JSON only.
	Path string
	// Name is the response header name. Header only.
	Name string
	// Value is the expected JSON or header value. JSON and header only.
	Value string
	// Pattern is the body regular expression. Regex only.
	Pattern string
	// Within is the longest the response may take, lower than the check's
	// timeout. Response time only.
	Within time.Duration
}

// ConditionLabel is the name of a condition kind in forms and messages.
func ConditionLabel(kind string) string {
	switch kind {
	case ConditionJSON:
		return "JSON value"
	case ConditionRegex:
		return "Body regex"
	case ConditionResponseTime:
		return "Max response time"
	case ConditionHeader:
		return "Response header"
	}
	return kind
}

// canonical keeps only the fields c's kind uses.
func (c Condition) canonical() Condition {
	out := Condition{Kind: c.Kind}
	switch c.Kind {
	case ConditionJSON:
		out.Path, out.Value = c.Path, c.Value
	case ConditionRegex:
		out.Pattern = c.Pattern
	case ConditionResponseTime:
		out.Within = c.Within
	case ConditionHeader:
		out.Name, out.Value = c.Name, c.Value
	default:
		return c
	}
	return out
}

// Summary is a short, one-line form of c for the detail view. The expected
// value of a sensitive header (see SensitiveHeader) is masked.
func (c Condition) Summary() string {
	switch c.Kind {
	case ConditionJSON:
		if c.Value == "" {
			return "JSON " + c.Path + " exists"
		}
		return fmt.Sprintf("JSON %s = %q", c.Path, c.Value)
	case ConditionRegex:
		return "body matches /" + c.Pattern + "/"
	case ConditionResponseTime:
		return "response within " + ShortDuration(c.Within)
	case ConditionHeader:
		switch {
		case c.Value == "":
			return "header " + c.Name + " exists"
		case SensitiveHeader(c.Name):
			return "header " + c.Name + " = ••••"
		}
		return fmt.Sprintf("header %s = %q", c.Name, c.Value)
	}
	return c.Kind
}

// SensitiveHeader reports whether a header's values must never be shown:
// cookies, credentials and any name that mentions a token, secret, key,
// password or auth. Names are compared without regard to case.
func SensitiveHeader(name string) bool {
	n := strings.ToLower(name)
	switch n {
	case "set-cookie", "cookie", "authorization", "proxy-authorization":
		return true
	}
	for _, word := range []string{"token", "secret", "key", "password", "auth"} {
		if strings.Contains(n, word) {
			return true
		}
	}
	return false
}

// errJSONPath explains the path syntax.
var errJSONPath = errors.New("use names and array indexes separated by dots, like services.0.status")

// JSONPath splits a condition path into its segments. Every segment must be
// non-empty; a segment indexes an array when it is a number.
func JSONPath(path string) ([]string, error) {
	segments := strings.Split(path, ".")
	if slices.Contains(segments, "") {
		return nil, errJSONPath
	}
	return segments, nil
}

// validateConditions checks the conditions of an HTTP check with the given
// timeout. Messages name the condition by its 1-based position.
func validateConditions(conds []Condition, timeout time.Duration) []error {
	var errs []error
	if len(conds) > MaxConditions {
		errs = append(errs, fmt.Errorf("at most %d conditions are allowed", MaxConditions))
	}
	for i, c := range conds {
		for _, err := range validateCondition(c, timeout) {
			errs = append(errs, fmt.Errorf("condition %d: %w", i+1, err))
		}
	}
	return errs
}

func validateCondition(c Condition, timeout time.Duration) []error {
	var errs []error
	long := func(what, s string) {
		if len(s) > MaxConditionText {
			errs = append(errs, fmt.Errorf("%s must be at most %d bytes", what, MaxConditionText))
		}
	}
	switch c.Kind {
	case ConditionJSON:
		switch _, err := JSONPath(c.Path); {
		case c.Path == "":
			errs = append(errs, errors.New("JSON path is required"))
		case err != nil:
			errs = append(errs, fmt.Errorf("JSON path %q is not valid: %w", c.Path, err))
		}
		long("JSON path", c.Path)
		long("JSON value", c.Value)
	case ConditionRegex:
		if c.Pattern == "" {
			errs = append(errs, errors.New("body regex is required"))
		} else if _, err := regexp.Compile(c.Pattern); err != nil {
			reason := err.Error()
			var se *syntax.Error
			if errors.As(err, &se) {
				reason = se.Code.String()
			}
			errs = append(errs, fmt.Errorf("body regex %q is not valid: %s", c.Pattern, reason))
		}
		long("body regex", c.Pattern)
	case ConditionResponseTime:
		switch {
		case c.Within <= 0:
			errs = append(errs, errors.New("max response time must be greater than 0"))
		case timeout > 0 && c.Within >= timeout:
			errs = append(errs, fmt.Errorf("max response time (%s) must be lower than timeout (%s)", ShortDuration(c.Within), ShortDuration(timeout)))
		}
	case ConditionHeader:
		switch {
		case c.Name == "":
			errs = append(errs, errors.New("header name is required"))
		case !ValidHeaderName(c.Name):
			errs = append(errs, fmt.Errorf("header name %q is not valid", c.Name))
		}
		long("header value", c.Value)
	default:
		errs = append(errs, fmt.Errorf("kind %q must be %s", c.Kind, conditionKindList()))
	}
	return errs
}

// conditionKindList is "json, regex, response_time or header".
func conditionKindList() string {
	n := len(ConditionKinds)
	return strings.Join(ConditionKinds[:n-1], ", ") + " or " + ConditionKinds[n-1]
}

// ReadsBody reports whether running c reads the response body: it expects a
// text or has a JSON or body regex condition.
func (c Check) ReadsBody() bool {
	return c.ExpectText != "" || slices.ContainsFunc(c.Conditions, readsBody)
}

func readsBody(c Condition) bool { return c.Kind == ConditionJSON || c.Kind == ConditionRegex }
