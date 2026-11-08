package check

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/config"
)

// maxShown is how many terminal cells of a received or expected value a
// reason shows.
const maxShown = 40

// response is what conditions are evaluated against: the latency, headers and
// the body read so far (at most maxExpectRead bytes; truncated when the
// response is larger).
type response struct {
	latency   time.Duration
	header    http.Header
	body      []byte
	truncated bool
	// parsed is the decoded JSON body, decoded on first use.
	parsed  any
	decoded bool
	jsonErr string
}

// failedCondition returns the reason the first failing condition gives, or
// "" when all pass.
func failedCondition(conds []config.Condition, r *response) string {
	for _, c := range conds {
		if reason := r.evaluate(c); reason != "" {
			return reason
		}
	}
	return ""
}

func (r *response) evaluate(c config.Condition) string {
	switch c.Kind {
	case config.ConditionJSON:
		return r.jsonValue(c)
	case config.ConditionRegex:
		re, err := regexp.Compile(c.Pattern)
		if err != nil {
			return fmt.Sprintf("body regex /%s/ is not valid", shown(c.Pattern))
		}
		if re.Match(r.body) {
			return ""
		}
		if r.truncated {
			return fmt.Sprintf("body does not match /%s/ in the first 1 MiB", shown(c.Pattern))
		}
		return fmt.Sprintf("body does not match /%s/", shown(c.Pattern))
	case config.ConditionResponseTime:
		if r.latency > c.Within {
			return slowReason(r.latency, c.Within)
		}
		return ""
	case config.ConditionHeader:
		return r.headerValue(c)
	}
	return fmt.Sprintf("condition %q is not supported", c.Kind)
}

func (r *response) jsonValue(c config.Condition) string {
	if !r.decoded {
		r.decoded = true
		switch {
		case r.truncated:
			r.jsonErr = "the response is larger than 1 MiB and was not read"
		default:
			dec := json.NewDecoder(bytes.NewReader(r.body))
			dec.UseNumber()
			if err := dec.Decode(&r.parsed); err != nil || dec.More() {
				r.jsonErr = "the response is not JSON"
			}
		}
	}
	if r.jsonErr != "" {
		return fmt.Sprintf("JSON %s: %s", shown(c.Path), r.jsonErr)
	}
	segments, err := config.JSONPath(c.Path)
	if err != nil {
		return fmt.Sprintf("JSON path %q is not valid", shown(c.Path))
	}
	v, ok := lookup(r.parsed, segments)
	if !ok {
		return fmt.Sprintf("JSON %s missing", shown(c.Path))
	}
	got := jsonText(v)
	if c.Value == "" || strings.EqualFold(got, c.Value) {
		return ""
	}
	return fmt.Sprintf("JSON %s = %q, want %q", shown(c.Path), shown(got), shown(c.Value))
}

// lookup follows segments through objects (by key) and arrays (by index).
func lookup(v any, segments []string) (any, bool) {
	for _, s := range segments {
		switch node := v.(type) {
		case map[string]any:
			next, ok := node[s]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			i, err := strconv.Atoi(s)
			if err != nil || i < 0 || i >= len(node) || strconv.Itoa(i) != s {
				return nil, false
			}
			v = node[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// jsonText is the text a JSON value is compared as: a string without quotes,
// anything else as its JSON form.
func jsonText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case nil:
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func (r *response) headerValue(c config.Condition) string {
	values := r.header.Values(c.Name)
	if len(values) == 0 {
		return fmt.Sprintf("header %s missing", c.Name)
	}
	if c.Value == "" {
		return ""
	}
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), c.Value) {
			return ""
		}
	}
	if config.SensitiveHeader(c.Name) {
		return fmt.Sprintf("header %s does not match", c.Name)
	}
	return fmt.Sprintf("header %s = %q, want %q", c.Name, shown(values[0]), shown(c.Value))
}

// slowReason is the reason a response took longer than limit. The time taken
// is rounded to 10ms from a second up and to 1ms below.
func slowReason(took, limit time.Duration) string {
	if took >= time.Second {
		took = took.Round(10 * time.Millisecond)
	} else {
		took = took.Round(time.Millisecond)
	}
	return fmt.Sprintf("response took %s, limit %s", config.ShortDuration(took), config.ShortDuration(limit))
}

// shown makes a value safe and short for a reason: control characters become
// spaces and the text is cut to maxShown cells with an ellipsis.
func shown(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return ansi.Truncate(s, maxShown, "…")
}
