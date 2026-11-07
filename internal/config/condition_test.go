package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

func conditionCheck(conds ...config.Condition) config.Check {
	return config.Check{Name: "api", URL: "https://example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200, Conditions: conds}
}

func TestValidateConditions(t *testing.T) {
	jsonCond := func(path, value string) config.Condition {
		return config.Condition{Kind: config.ConditionJSON, Path: path, Value: value}
	}
	regex := func(p string) config.Condition { return config.Condition{Kind: config.ConditionRegex, Pattern: p} }
	within := func(d time.Duration) config.Condition {
		return config.Condition{Kind: config.ConditionResponseTime, Within: d}
	}
	header := func(name, value string) config.Condition {
		return config.Condition{Kind: config.ConditionHeader, Name: name, Value: value}
	}
	for _, tc := range []struct {
		name  string
		conds []config.Condition
		want  string
	}{
		{"none", nil, ""},
		{"every kind", []config.Condition{jsonCond("status", "ok"), regex(`(?i)ready`), within(800 * time.Millisecond), header("X-Version", "3")}, ""},
		{"several json and headers", []config.Condition{jsonCond("a", "1"), jsonCond("services.0.status", "ok"), header("X-A", "1"), header("X-A", "2")}, ""},
		{"array index path", []config.Condition{jsonCond("services.0.status", "ok")}, ""},
		{"empty path", []config.Condition{jsonCond("", "ok")}, "condition 1: JSON path is required"},
		{"double dot", []config.Condition{jsonCond("a..b", "ok")}, `condition 1: JSON path "a..b" is not valid: use names and array indexes separated by dots, like services.0.status`},
		{"leading dot", []config.Condition{jsonCond(".a", "ok")}, `condition 1: JSON path ".a" is not valid`},
		{"trailing dot", []config.Condition{jsonCond("a.", "ok")}, `condition 1: JSON path "a." is not valid`},
		{"second condition named", []config.Condition{jsonCond("a", "1"), jsonCond("", "1")}, "condition 2: JSON path is required"},
		{"empty regex", []config.Condition{regex("")}, "condition 1: body regex is required"},
		{"invalid regex", []config.Condition{regex("(")}, `condition 1: body regex "(" is not valid: missing closing )`},
		{"zero time", []config.Condition{within(0)}, "condition 1: max response time must be greater than 0"},
		{"time equals timeout", []config.Condition{within(5 * time.Second)}, "condition 1: max response time (5s) must be lower than timeout (5s)"},
		{"time above timeout", []config.Condition{within(time.Minute)}, "condition 1: max response time (1m) must be lower than timeout (5s)"},
		{"time below timeout", []config.Condition{within(4999 * time.Millisecond)}, ""},
		{"empty header name", []config.Condition{header("", "x")}, "condition 1: header name is required"},
		{"invalid header name", []config.Condition{header("X A", "x")}, `condition 1: header name "X A" is not valid`},
		{"header presence", []config.Condition{header("X-A", "")}, ""},
		{"unknown kind", []config.Condition{{Kind: "xml"}}, `condition 1: kind "xml" must be json, regex, response_time or header`},
		{"long value", []config.Condition{jsonCond("a", strings.Repeat("v", config.MaxConditionText+1))}, "condition 1: JSON value must be at most 1024 bytes"},
		{"long pattern", []config.Condition{regex(strings.Repeat("v", config.MaxConditionText+1))}, "condition 1: body regex must be at most 1024 bytes"},
		{"too many", make([]config.Condition, config.MaxConditions+1), "at most 32 conditions are allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "too many" {
				for i := range tc.conds {
					tc.conds[i] = header("X-A", "")
				}
			}
			err := config.ValidateCheck(conditionCheck(tc.conds...))
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateCheck() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestConditionsNeedABodyAndHTTP(t *testing.T) {
	c := conditionCheck(config.Condition{Kind: config.ConditionJSON, Path: "a", Value: "1"})
	c.Method = "HEAD"
	if err := config.ValidateCheck(c); err == nil || !strings.Contains(err.Error(), "JSON and body regex conditions need a method that returns a body; HEAD does not") {
		t.Fatalf("HEAD with JSON = %v", err)
	}
	c.Conditions = []config.Condition{{Kind: config.ConditionHeader, Name: "X-A"}, {Kind: config.ConditionResponseTime, Within: time.Second}}
	if err := config.ValidateCheck(c); err != nil {
		t.Fatalf("HEAD with header and time conditions = %v", err)
	}
	tcp := config.Check{Name: "db", Kind: config.CheckTCP, Host: "db", Port: 5432, Interval: time.Minute, Timeout: time.Second,
		Conditions: []config.Condition{{Kind: config.ConditionResponseTime, Within: 100 * time.Millisecond}}}
	if err := config.ValidateCheck(tcp); err == nil || !strings.Contains(err.Error(), "conditions are only for http checks") {
		t.Fatalf("TCP with conditions = %v", err)
	}
	beat := config.Check{Name: "job", Kind: config.CheckHeartbeat, Period: time.Hour,
		Conditions: []config.Condition{{Kind: config.ConditionHeader, Name: "X-A"}}}
	if err := config.ValidateCheck(beat); err == nil || !strings.Contains(err.Error(), "conditions are not used by heartbeat checks") {
		t.Fatalf("heartbeat with conditions = %v", err)
	}
}

func TestConditionsAreOptional(t *testing.T) {
	if err := config.ValidateCheck(conditionCheck()); err != nil {
		t.Fatal(err)
	}
	if c := conditionCheck().Canonical(); c.Conditions != nil {
		t.Fatalf("empty conditions canonical = %#v, want nil", c.Conditions)
	}
	if c := conditionCheck([]config.Condition{}...).Canonical(); c.Conditions != nil {
		t.Fatalf("empty list canonical = %#v, want nil", c.Conditions)
	}
}

func TestConditionsAreOrderedDefinition(t *testing.T) {
	a := config.Condition{Kind: config.ConditionJSON, Path: "a", Value: "1"}
	b := config.Condition{Kind: config.ConditionHeader, Name: "X-B", Value: "2"}
	base := conditionCheck(a, b)
	for name, other := range map[string]config.Check{
		"order":   conditionCheck(b, a),
		"value":   conditionCheck(config.Condition{Kind: config.ConditionJSON, Path: "a", Value: "2"}, b),
		"removed": conditionCheck(a),
		"none":    conditionCheck(),
	} {
		if base.SameDefinition(other) {
			t.Errorf("%s: SameDefinition = true", name)
		}
	}
	if !base.SameDefinition(conditionCheck(a, b)) {
		t.Error("equal conditions are a different definition")
	}
	// Fields another kind uses are dropped, so they never make a difference.
	stray := a
	stray.Pattern, stray.Name, stray.Within = "x", "X-A", time.Second
	if !base.SameDefinition(conditionCheck(stray, b)) {
		t.Error("stray fields of another kind changed the definition")
	}
	// Canonical copies the list.
	c := conditionCheck(a, b)
	canon := c.Canonical()
	canon.Conditions[0].Value = "changed"
	if c.Conditions[0].Value != "1" {
		t.Error("Canonical shares the conditions slice")
	}
}

func TestConditionSummaryAndSensitiveHeaders(t *testing.T) {
	for _, tc := range []struct {
		c    config.Condition
		want string
	}{
		{config.Condition{Kind: config.ConditionJSON, Path: "status", Value: "ok"}, `JSON status = "ok"`},
		{config.Condition{Kind: config.ConditionJSON, Path: "checks.db", Value: ""}, "JSON checks.db exists"},
		{config.Condition{Kind: config.ConditionRegex, Pattern: `(?i)ready`}, "body matches /(?i)ready/"},
		{config.Condition{Kind: config.ConditionResponseTime, Within: 800 * time.Millisecond}, "response within 800ms"},
		{config.Condition{Kind: config.ConditionHeader, Name: "X-Version", Value: "3"}, `header X-Version = "3"`},
		{config.Condition{Kind: config.ConditionHeader, Name: "X-Version"}, "header X-Version exists"},
		{config.Condition{Kind: config.ConditionHeader, Name: "Authorization", Value: "Bearer x"}, "header Authorization = ••••"},
	} {
		if got := tc.c.Summary(); got != tc.want {
			t.Errorf("Summary(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
	for name, want := range map[string]bool{
		"Set-Cookie": true, "authorization": true, "Proxy-Authorization": true, "Cookie": true,
		"X-Api-Key": true, "X-Auth-Token": true, "X-Client-Secret": true,
		"X-Version": false, "Content-Type": false, "ETag": false,
	} {
		if got := config.SensitiveHeader(name); got != want {
			t.Errorf("SensitiveHeader(%q) = %v, want %v", name, got, want)
		}
	}
	if len(config.ConditionKinds) != 4 || config.ConditionLabel(config.ConditionJSON) != "JSON value" || config.ConditionLabel(config.ConditionRegex) != "Body regex" ||
		config.ConditionLabel(config.ConditionResponseTime) != "Max response time" || config.ConditionLabel(config.ConditionHeader) != "Response header" {
		t.Errorf("kinds %v labels wrong", config.ConditionKinds)
	}
}

func TestJSONPathSegments(t *testing.T) {
	got, err := config.JSONPath("services.0.status")
	if err != nil || strings.Join(got, "|") != "services|0|status" {
		t.Fatalf("JSONPath = %q, %v", got, err)
	}
	if _, err := config.JSONPath("a..b"); err == nil {
		t.Fatal("malformed path accepted")
	}
}
