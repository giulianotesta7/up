package check

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

const conditionBody = `{"status":"OK","count":3,"ready":true,"flag":"true","none":null,"version":"2",
"services":[{"status":"ok"},{"status":"degraded"}],"checks":{"db":{}}}`

// conditionServer answers 200 with body and the given response headers.
func conditionServer(t *testing.T, body string, headers map[string][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for name, values := range headers {
			for _, v := range values {
				w.Header().Add(name, v)
			}
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func runConditions(t *testing.T, url string, conds ...config.Condition) Result {
	t.Helper()
	c := checkFor(url, 200, 2*time.Second)
	c.Conditions = conds
	if err := config.ValidateCheck(c); err != nil {
		t.Fatalf("invalid test check: %v", err)
	}
	return newChecker().Run(context.Background(), c)
}

func jsonIs(path, value string) config.Condition {
	return config.Condition{Kind: config.ConditionJSON, Path: path, Value: value}
}

func headerIs(name, value string) config.Condition {
	return config.Condition{Kind: config.ConditionHeader, Name: name, Value: value}
}

func matches(pattern string) config.Condition {
	return config.Condition{Kind: config.ConditionRegex, Pattern: pattern}
}

func within(d time.Duration) config.Condition {
	return config.Condition{Kind: config.ConditionResponseTime, Within: d}
}

func TestRunJSONConditionsCompareAsTextIgnoringCase(t *testing.T) {
	srv := conditionServer(t, conditionBody, nil)
	for _, cond := range []config.Condition{
		jsonIs("status", "ok"), jsonIs("status", "OK"),
		jsonIs("count", "3"),
		jsonIs("ready", "true"), jsonIs("ready", "TRUE"),
		jsonIs("flag", "true"),
		jsonIs("none", "null"),
		jsonIs("services.0.status", "ok"), jsonIs("services.1.status", "Degraded"),
		jsonIs("checks.db", "{}"),
		jsonIs("checks.db", ""),
	} {
		if res := runConditions(t, srv.URL, cond); res.Status != StatusUp || res.Error != "" {
			t.Errorf("%+v: result = %+v, want up", cond, res)
		}
	}
}

func TestRunJSONConditionReasons(t *testing.T) {
	srv := conditionServer(t, conditionBody, nil)
	for _, tc := range []struct {
		cond config.Condition
		want string
	}{
		{jsonIs("services.1.status", "ok"), `JSON services.1.status = "degraded", want "ok"`},
		{jsonIs("count", "3.0"), `JSON count = "3", want "3.0"`},
		{jsonIs("ready", "false"), `JSON ready = "true", want "false"`},
		{jsonIs("checks.db.healthy", "true"), "JSON checks.db.healthy missing"},
		{jsonIs("checks.db.healthy", ""), "JSON checks.db.healthy missing"},
		{jsonIs("services.5.status", "ok"), "JSON services.5.status missing"},
		{jsonIs("services.first", "ok"), "JSON services.first missing"},
		{jsonIs("status.inner", "ok"), "JSON status.inner missing"},
	} {
		res := runConditions(t, srv.URL, tc.cond)
		if res.Status != StatusDown || res.Error != tc.want || res.StatusCode != 200 {
			t.Errorf("%+v: result = %+v, want down %q", tc.cond, res, tc.want)
		}
	}
	notJSON := conditionServer(t, "<html>ok</html>", nil)
	if res := runConditions(t, notJSON.URL, jsonIs("status", "ok")); res.Error != "JSON status: the response is not JSON" {
		t.Errorf("not JSON: %+v", res)
	}
	escape := conditionServer(t, `{"status":"a\u001b[31mb"}`, nil)
	if res := runConditions(t, escape.URL, jsonIs("status", "ok")); res.Error != `JSON status = "a [31mb", want "ok"` {
		t.Errorf("control characters reach the reason: %q", res.Error)
	}
	big := conditionServer(t, `{"status":"ok","pad":"`+strings.Repeat("a", maxExpectRead)+`"}`, nil)
	if res := runConditions(t, big.URL, jsonIs("status", "ok")); res.Error != "JSON status: the response is larger than 1 MiB and was not read" {
		t.Errorf("larger than the cap: %+v", res)
	}
}

func TestRunRegexConditionUsesItsOwnFlags(t *testing.T) {
	srv := conditionServer(t, "service Ready\nversion 2.1", nil)
	for _, pattern := range []string{`Ready`, `(?i)ready`, `version \d+\.\d+`, `(?m)^version`} {
		if res := runConditions(t, srv.URL, matches(pattern)); res.Status != StatusUp {
			t.Errorf("%q: result = %+v, want up", pattern, res)
		}
	}
	if res := runConditions(t, srv.URL, matches(`ready`)); res.Status != StatusDown || res.Error != "body does not match /ready/" {
		t.Errorf("case-sensitive miss: %+v", res)
	}
	long := strings.Repeat("x", 60)
	if res := runConditions(t, srv.URL, matches(long)); res.Error != "body does not match /"+strings.Repeat("x", 39)+"…/" {
		t.Errorf("long pattern reason: %q", res.Error)
	}
	big := conditionServer(t, strings.Repeat("a", maxExpectRead)+"needle", nil)
	if res := runConditions(t, big.URL, matches("needle")); res.Error != "body does not match /needle/ in the first 1 MiB" {
		t.Errorf("larger than the cap: %+v", res)
	}
}

func TestRunResponseTimeCondition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
	}))
	defer srv.Close()
	res := runConditions(t, srv.URL, within(20*time.Millisecond))
	if res.Status != StatusDown || !strings.HasPrefix(res.Error, "response took ") || !strings.HasSuffix(res.Error, "ms, limit 20ms") || res.Latency < 120*time.Millisecond {
		t.Fatalf("slow response: %+v", res)
	}
	if res := runConditions(t, srv.URL, within(time.Second)); res.Status != StatusUp {
		t.Fatalf("fast enough: %+v", res)
	}
}

func TestResponseTimeReasonFormat(t *testing.T) {
	for _, tc := range []struct {
		took, limit time.Duration
		want        string
	}{
		{1243 * time.Millisecond, 800 * time.Millisecond, "response took 1.24s, limit 800ms"},
		{2 * time.Second, time.Second, "response took 2s, limit 1s"},
		{312456 * time.Microsecond, 300 * time.Millisecond, "response took 312ms, limit 300ms"},
	} {
		if got := slowReason(tc.took, tc.limit); got != tc.want {
			t.Errorf("slowReason(%s, %s) = %q, want %q", tc.took, tc.limit, got, tc.want)
		}
	}
}

func TestRunHeaderConditions(t *testing.T) {
	srv := conditionServer(t, "ok", map[string][]string{
		"X-Version":     {"2"},
		"X-Mode":        {"Primary"},
		"X-Many":        {"a", "b"},
		"Set-Cookie":    {"session=s3cret"},
		"X-Api-Key":     {"k3y"},
		"X-Long":        {strings.Repeat("v", 100)},
		"Authorization": {"Bearer s3cret"},
	})
	for _, cond := range []config.Condition{headerIs("x-version", "2"), headerIs("X-Mode", "primary"), headerIs("X-Many", "b"), headerIs("X-Version", "")} {
		if res := runConditions(t, srv.URL, cond); res.Status != StatusUp {
			t.Errorf("%+v: result = %+v, want up", cond, res)
		}
	}
	for _, tc := range []struct {
		cond config.Condition
		want string
	}{
		{headerIs("X-Version", "3"), `header X-Version = "2", want "3"`},
		{headerIs("X-Missing", "1"), "header X-Missing missing"},
		{headerIs("X-Missing", ""), "header X-Missing missing"},
		{headerIs("Set-Cookie", "session=other"), "header Set-Cookie does not match"},
		{headerIs("authorization", "Bearer other"), "header authorization does not match"},
		{headerIs("X-Api-Key", "other"), "header X-Api-Key does not match"},
		{headerIs("X-Long", "short"), `header X-Long = "` + strings.Repeat("v", 39) + `…", want "short"`},
	} {
		res := runConditions(t, srv.URL, tc.cond)
		if res.Status != StatusDown || res.Error != tc.want {
			t.Errorf("%+v: result = %+v, want down %q", tc.cond, res, tc.want)
		}
		for _, secret := range []string{"s3cret", "k3y"} {
			if strings.Contains(res.Error, secret) {
				t.Errorf("reason %q shows a sensitive value", res.Error)
			}
		}
	}
}

func TestRunConditionsAllMustPassFirstFailureNamed(t *testing.T) {
	srv := conditionServer(t, conditionBody, map[string][]string{"X-Version": {"2"}})
	res := runConditions(t, srv.URL, headerIs("X-Version", "2"), jsonIs("status", "degraded"), matches("absent"), headerIs("X-Version", "9"))
	if res.Status != StatusDown || res.Error != `JSON status = "OK", want "degraded"` {
		t.Fatalf("first failure: %+v", res)
	}
	res = runConditions(t, srv.URL, headerIs("X-Version", "2"), jsonIs("status", "ok"), matches(`"count":3`), within(time.Second), jsonIs("services.0.status", "OK"))
	if res.Status != StatusUp || res.Error != "" {
		t.Fatalf("all pass: %+v", res)
	}
	// A wrong status and missing expected text still win, with their own messages.
	c := checkFor(srv.URL, 201, time.Second)
	c.Conditions = []config.Condition{jsonIs("status", "degraded")}
	if res := newChecker().Run(context.Background(), c); res.Error != "unexpected status 200 (expected 201)" {
		t.Fatalf("status first: %+v", res)
	}
	c.ExpectedStatus, c.ExpectText = 200, `"STATUS":"OK"`
	if res := newChecker().Run(context.Background(), c); res.Error != "expected text not found" {
		t.Fatalf("expected text stays exact and first: %+v", res)
	}
	c.ExpectText = `"status":"OK"`
	if res := newChecker().Run(context.Background(), c); res.Error != `JSON status = "OK", want "degraded"` {
		t.Fatalf("conditions after expected text: %+v", res)
	}
}

func TestRunConditionBodyReadIsInsideTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	c := checkFor(srv.URL, 200, 150*time.Millisecond)
	c.Conditions = []config.Condition{jsonIs("status", "ok")}
	res := newChecker().Run(context.Background(), c)
	if res.Status != StatusDown || res.Error != "timeout after 150ms" {
		t.Fatalf("result = %+v", res)
	}
}
