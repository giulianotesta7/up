package check

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

// Regression probes from the #94 verification, beyond condition_test.go.

func TestProbeJSONTextForm(t *testing.T) {
	srv := conditionServer(t, `{"mode":"Ok","n":3,"f":3.0,"t":true,"s":"true","list":[{"a":1}],"arr":[[10,20]]}`, nil)
	for _, cond := range []config.Condition{
		jsonIs("mode", "ok"), jsonIs("mode", "OK"), jsonIs("mode", "Ok"),
		jsonIs("n", "3"),
		jsonIs("t", "true"), jsonIs("s", "true"), jsonIs("s", "True"),
		jsonIs("f", "3.0"),
		jsonIs("list.0.a", "1"),
		jsonIs("arr.0.1", "20"),
		jsonIs("arr", "[[10,20]]"),
	} {
		if res := runConditions(t, srv.URL, cond); res.Status != StatusUp || res.Error != "" {
			t.Errorf("%+v: result = %+v, want up", cond, res)
		}
	}
	for _, tc := range []struct {
		cond config.Condition
		want string
	}{
		// Numbers compare by their literal text.
		{jsonIs("f", "3"), `JSON f = "3.0", want "3"`},
		{jsonIs("t", "yes"), `JSON t = "true", want "yes"`},
		{jsonIs("mode", "\"ok\""), `JSON mode = "Ok", want "\"ok\""`},
	} {
		if res := runConditions(t, srv.URL, tc.cond); res.Status != StatusDown || res.Error != tc.want {
			t.Errorf("%+v: result = %+v, want down %q", tc.cond, res, tc.want)
		}
	}
}

func TestProbeJSONArrayIndexAndMissingReasons(t *testing.T) {
	srv := conditionServer(t, `{"services":[{"status":"ok"}],"name":"api"}`, nil)
	for _, path := range []string{
		"services.1.status",  // past the end
		"services.-1.status", // negative
		"services.01.status", // not the plain form of an index
		"services.+0.status",
		"name.0",            // index into a string
		"services.0.x",      // missing key in an element
		"Services.0.status", // keys keep their case
	} {
		want := "JSON " + path + " missing"
		if res := runConditions(t, srv.URL, jsonIs(path, "ok")); res.Status != StatusDown || res.Error != want {
			t.Errorf("%s: result = %+v, want down %q", path, res, want)
		}
	}
	top := conditionServer(t, `[{"status":"ok"}]`, nil)
	if res := runConditions(t, top.URL, jsonIs("0.status", "OK")); res.Status != StatusUp {
		t.Errorf("top-level array: %+v", res)
	}
}

func TestProbeExpectedTextStaysExactWhileJSONIgnoresCase(t *testing.T) {
	srv := conditionServer(t, `{"mode":"Ok"}`, nil)
	c := checkFor(srv.URL, 200, 2*time.Second)
	c.ExpectText = `"mode":"ok"`
	if res := newChecker().Run(context.Background(), c); res.Status != StatusDown || res.Error != "expected text not found" {
		t.Fatalf("expected text ignored case: %+v", res)
	}
	c.ExpectText = `"mode":"Ok"`
	c.Conditions = []config.Condition{jsonIs("mode", "OK")}
	if res := newChecker().Run(context.Background(), c); res.Status != StatusUp {
		t.Fatalf("JSON value with other case: %+v", res)
	}
}

func TestProbeHeaderReasons(t *testing.T) {
	srv := conditionServer(t, "ok", map[string][]string{
		"Set-Cookie":    {"a=s3cret", "b=s3cret2"},
		"Authorization": {"Bearer s3cret"},
		"X-Session-Key": {"s3cret"},
		"X-Wide":        {strings.Repeat("界", 30)},
		"X-Tab":         {"a\tb"},
		"X-Many":        {"one", "two", "three"},
	})
	for _, cond := range []config.Condition{
		headerIs("X-Many", "one"), headerIs("X-Many", "THREE"),
		headerIs("Set-Cookie", "b=s3cret2"),
		headerIs("x-session-key", "S3CRET"),
	} {
		if res := runConditions(t, srv.URL, cond); res.Status != StatusUp {
			t.Errorf("%+v: result = %+v, want up", cond, res)
		}
	}
	for _, tc := range []struct {
		cond config.Condition
		want string
	}{
		{headerIs("Set-Cookie", "c=other"), "header Set-Cookie does not match"},
		{headerIs("Authorization", "Bearer other"), "header Authorization does not match"},
		{headerIs("X-Session-Key", "other"), "header X-Session-Key does not match"},
		{headerIs("X-Wide", "x"), `header X-Wide = "` + strings.Repeat("界", 19) + `…", want "x"`},
		{headerIs("X-Tab", "ab"), `header X-Tab = "a b", want "ab"`},
		{headerIs("X-Many", "four"), `header X-Many = "one", want "four"`},
	} {
		res := runConditions(t, srv.URL, tc.cond)
		if res.Status != StatusDown || res.Error != tc.want {
			t.Errorf("%+v: result = %+v, want down %q", tc.cond, res, tc.want)
		}
		if strings.Contains(res.Error, "s3cret") {
			t.Errorf("reason %q shows a sensitive value", res.Error)
		}
	}
}
