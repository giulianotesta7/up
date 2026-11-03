package remote

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

type writeFake struct {
	calls int
	err   error
	name  string
	until time.Time
}

func (f *writeFake) AddCheck(_ context.Context, c config.Check) error {
	f.calls++
	f.name = c.Name
	return f.err
}
func (f *writeFake) UpdateCheck(_ context.Context, c config.Check) error {
	f.calls++
	f.name = c.Name
	return f.err
}
func (f *writeFake) DeleteCheck(_ context.Context, n string) error {
	f.calls++
	f.name = n
	return f.err
}
func (f *writeFake) SetMute(_ context.Context, n string, until time.Time, _ bool) error {
	f.calls++
	f.name = n
	f.until = until
	return f.err
}
func (f *writeFake) Unmute(_ context.Context, n string, _ time.Time) error {
	f.calls++
	f.name = n
	return f.err
}

func TestWriteRoutes(t *testing.T) {
	f := &writeFake{}
	h, _ := testHandler(t, func(m *http.ServeMux) {
		m.Handle("/api/v1/checks", WriteRoutes(f))
		m.Handle("/api/v1/checks/", WriteRoutes(f))
	})
	headers := bearer(testToken)
	headers["Content-Type"] = "application/json"
	valid := `{"name":"site","url":"https://example.com","interval_ms":60000,"timeout_ms":1000,"expected_status":200}`
	for _, tc := range []struct {
		method, path, body string
		status             int
		message            string
	}{
		{"POST", "/api/v1/checks", valid, 201, ""},
		{"PUT", "/api/v1/checks/site", valid, 204, ""},
		{"PUT", "/api/v1/checks/site", `{"name":"other"}`, 400, "name must match path"},
		{"PUT", "/api/v1/checks/site/mute", `{}`, 400, "duration_ms is required"},
		{"PUT", "/api/v1/checks/site/mute", `{"duration_ms":59999}`, 400, "duration must be between 1m and 7d"},
		{"PUT", "/api/v1/checks/site/mute", `{"duration_ms":60000}`, 204, ""},
		{"PUT", "/api/v1/checks/site/mute", `{"duration_ms":604800000}`, 204, ""},
		{"PUT", "/api/v1/checks/site/mute", `{"duration_ms":604800001}`, 400, "duration must be between 1m and 7d"},
		{"PUT", "/api/v1/checks/site/mute", `{"duration_ms":60000,"unknown":1}`, 400, "invalid request body"},
		{"POST", "/api/v1/checks", `null`, 400, "invalid request body"},
		{"DELETE", "/api/v1/checks/site/mute", "", 204, ""},
		{"DELETE", "/api/v1/checks/site", "", 204, ""},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			r := do(h, tc.method, tc.path, strings.NewReader(tc.body), headers)
			if r.Code != tc.status || (tc.message != "" && r.Body.String() != `{"error":"`+tc.message+"\"}\n") {
				t.Fatalf("got %d %s", r.Code, r.Body)
			}
		})
	}
	for _, p := range []string{"/api/v1/checks", "/api/v1/checks/site", "/api/v1/checks/site/mute"} {
		before := f.calls
		r := do(h, "POST", p, nil, nil)
		if r.Code != 401 || f.calls != before {
			t.Fatal("unauthenticated reached store")
		}
		r = do(h, "GET", p, nil, headers)
		if r.Code != 405 || r.Header().Get("Allow") == "" {
			t.Fatalf("method %s: %d", p, r.Code)
		}
	}
	for _, n := range []struct{ path, want string }{{"space%20name", "space name"}, {"%E2%98%83", "☃"}, {"a%2Fb", "a/b"}} {
		r := do(h, "DELETE", "/api/v1/checks/"+n.path, nil, headers)
		if r.Code != 204 || f.name != n.want {
			t.Fatalf("path %s: %d %q", n.path, r.Code, f.name)
		}
	}
	for _, tc := range []struct {
		err    error
		status int
		text   string
	}{{store.ErrNotFound, 404, store.ErrNotFound.Error()}, {store.ErrExists, 409, store.ErrExists.Error()}, {store.ErrValidation, 400, store.ErrValidation.Error()}, {errors.New("secret"), 500, "internal error"}} {
		f.err = tc.err
		r := do(h, "POST", "/api/v1/checks", strings.NewReader(valid), headers)
		if r.Code != tc.status || r.Body.String() != `{"error":"`+tc.text+"\"}\n" {
			t.Fatalf("mapping: %d %s", r.Code, r.Body)
		}
	}
}

func TestWriteRoutesRejectBeforeTheStore(t *testing.T) {
	f := &writeFake{}
	h, _ := testHandler(t, func(m *http.ServeMux) {
		m.Handle("/api/v1/checks", WriteRoutes(f))
		m.Handle("/api/v1/checks/", WriteRoutes(f))
	})
	headers := bearer(testToken)
	headers["Content-Type"] = "application/json"
	body := func(name string, interval, timeout int64) string {
		return fmt.Sprintf(`{"name":%q,"url":"https://example.com","interval_ms":%d,"timeout_ms":%d,"expected_status":200}`, name, interval, timeout)
	}
	for _, tc := range []struct {
		name, method, path, body string
		status                   int
		message                  string
	}{
		{"interval overflow", "POST", "/api/v1/checks", body("a", math.MaxInt64, 1000), 400, "interval_ms is out of range"},
		{"timeout overflow", "POST", "/api/v1/checks", body("a", 60000, math.MaxInt64), 400, "timeout_ms is out of range"},
		{"update overflow", "PUT", "/api/v1/checks/a", body("a", math.MaxInt64, 1000), 400, "interval_ms is out of range"},
		{"dot name", "POST", "/api/v1/checks", body(".", 60000, 1000), 400, `name cannot be "." or ".." over the API`},
		{"dotdot name", "POST", "/api/v1/checks", body("..", 60000, 1000), 400, `name cannot be "." or ".." over the API`},
		{"nested path", "DELETE", "/api/v1/checks/a/b", "", 404, "not found"},
		{"empty name", "DELETE", "/api/v1/checks/", "", 404, "not found"},
		{"unknown suffix", "PUT", "/api/v1/checks/a/other", "{}", 404, "not found"},
	} {
		rec := do(h, tc.method, tc.path, strings.NewReader(tc.body), headers)
		if rec.Code != tc.status || rec.Body.String() != fmt.Sprintf("{\"error\":%q}\n", tc.message) {
			t.Errorf("%s: got %d %q, want %d %q", tc.name, rec.Code, rec.Body, tc.status, tc.message)
		}
	}
	if f.calls != 0 {
		t.Fatalf("store was called %d times for rejected requests", f.calls)
	}
}

func TestWriteRoutesNamesAreOneUnescapedSegment(t *testing.T) {
	f := &writeFake{}
	h, _ := testHandler(t, func(m *http.ServeMux) {
		m.Handle("/api/v1/checks", WriteRoutes(f))
		m.Handle("/api/v1/checks/", WriteRoutes(f))
	})
	for _, name := range []string{"my site", "café ☕", "a/b", "100%", "a?b#c"} {
		f.name = ""
		rec := do(h, "DELETE", "/api/v1/checks/"+url.PathEscape(name), nil, bearer(testToken))
		if rec.Code != 204 || f.name != name {
			t.Errorf("name %q: got %d and store name %q", name, rec.Code, f.name)
		}
	}
}
