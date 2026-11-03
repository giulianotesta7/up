package remote

import (
	"bytes"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "up_" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE"

func testHandler(t *testing.T, routes func(*http.ServeMux)) (http.Handler, *int) {
	t.Helper()
	var hits int
	mux := http.NewServeMux()
	if routes != nil {
		routes(mux)
	}
	mux.HandleFunc("POST /api/v1/echo", func(w http.ResponseWriter, r *http.Request) {
		hits++
		var in struct{ Name string }
		if !DecodeJSON(w, r, &in) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	dir := t.TempDir()
	if err := SaveToken(dir+"/t", testToken); err != nil {
		t.Fatal(err)
	}
	v, err := LoadVerifier(dir + "/t")
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(Options{Verifier: v, Version: "9.9.9", Routes: mux}), &hits
}

func do(h http.Handler, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func TestPingNeedsTheToken(t *testing.T) {
	h, _ := testHandler(t, nil)
	rec := do(h, "GET", "/api/v1/ping", nil, bearer(testToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"9.9.9"`) {
		t.Fatalf("ping = %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("missing hardening headers: %v", rec.Header())
	}
}

func TestEveryAuthFailureLooksTheSame(t *testing.T) {
	h, hits := testHandler(t, nil)
	cases := map[string]map[string]string{
		"missing":      nil,
		"wrong":        bearer("up_wrong"),
		"empty":        {"Authorization": "Bearer "},
		"basic":        {"Authorization": "Basic " + testToken},
		"raw":          {"Authorization": testToken},
		"almost right": bearer(testToken + "x"),
	}
	var first string
	for name, headers := range cases {
		rec := do(h, "POST", "/api/v1/echo", strings.NewReader(`{"name":"a"}`), headers)
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: status = %d, www = %q", name, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
		if first == "" {
			first = rec.Body.String()
		}
		if rec.Body.String() != first {
			t.Errorf("%s: body differs: %q vs %q", name, rec.Body, first)
		}
		if strings.Contains(rec.Body.String(), testToken) {
			t.Errorf("%s: response echoes the token", name)
		}
	}
	if *hits != 0 {
		t.Fatalf("handler ran %d times for unauthenticated requests", *hits)
	}
}

func TestTokenInQueryOrSecondHeaderIsNotAccepted(t *testing.T) {
	h, _ := testHandler(t, nil)
	if rec := do(h, "GET", "/api/v1/ping?token="+testToken, nil, nil); rec.Code != 401 {
		t.Errorf("query token: %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "/api/v1/ping", nil)
	req.Header.Add("Authorization", "Bearer "+testToken)
	req.Header.Add("Authorization", "Bearer other")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("two Authorization headers: %d", rec.Code)
	}
}

func TestUnauthenticatedUnknownPathsDoNotLeakRoutes(t *testing.T) {
	h, _ := testHandler(t, nil)
	if rec := do(h, "GET", "/nope", nil, nil); rec.Code != 401 {
		t.Errorf("unknown path without token = %d, want 401", rec.Code)
	}
	if rec := do(h, "GET", "/nope", nil, bearer(testToken)); rec.Code != 404 {
		t.Errorf("unknown path with token = %d, want 404", rec.Code)
	}
}

func TestDecodeJSONIsStrict(t *testing.T) {
	h, hits := testHandler(t, nil)
	json := map[string]string{"Content-Type": "application/json"}
	auth := func(extra map[string]string) map[string]string {
		m := bearer(testToken)
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		name string
		body string
		ct   map[string]string
		want int
	}{
		{"valid", `{"name":"a"}`, json, 204},
		{"unknown field", `{"name":"a","admin":true}`, json, 400},
		{"trailing data", `{"name":"a"}{"name":"b"}`, json, 400},
		{"malformed", `{"name":`, json, 400},
		{"wrong content type", `{"name":"a"}`, map[string]string{"Content-Type": "text/plain"}, 415},
		{"no content type", `{"name":"a"}`, nil, 415},
	}
	for _, c := range cases {
		before := *hits
		rec := do(h, "POST", "/api/v1/echo", strings.NewReader(c.body), auth(c.ct))
		if rec.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, rec.Code, c.want, rec.Body)
		}
		_ = before
	}
}

func TestBodyLimitAppliesToKnownAndChunkedBodies(t *testing.T) {
	h, _ := testHandler(t, nil)
	big := `{"name":"` + strings.Repeat("a", MaxBodyBytes+10) + `"}`
	headers := bearer(testToken)
	headers["Content-Type"] = "application/json"

	if rec := do(h, "POST", "/api/v1/echo", strings.NewReader(big), headers); rec.Code != 413 {
		t.Errorf("known length: status = %d, want 413", rec.Code)
	}

	req := httptest.NewRequest("POST", "/api/v1/echo", io.NopCloser(bytes.NewReader([]byte(big))))
	req.ContentLength = -1 // chunked: no declared length to check up front
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Errorf("chunked: status = %d, want 413", rec.Code)
	}
}

func TestServerIsHardened(t *testing.T) {
	srv := NewHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("timeouts must all be set: %+v", srv)
	}
	if srv.MaxHeaderBytes <= 0 || srv.MaxHeaderBytes > 64<<10 {
		t.Errorf("MaxHeaderBytes = %d", srv.MaxHeaderBytes)
	}
	if srv.TLSConfig == nil || srv.TLSConfig.MinVersion < tls.VersionTLS12 {
		t.Errorf("TLS must require 1.2 or newer: %+v", srv.TLSConfig)
	}
}

func TestListenPolicy(t *testing.T) {
	cases := []struct {
		addr        string
		tls, proxy  bool
		wantErr     bool
		description string
	}{
		{"127.0.0.1:8443", false, false, false, "loopback needs no TLS"},
		{"localhost:8443", false, false, false, "localhost needs no TLS"},
		{"[::1]:8443", false, false, false, "ipv6 loopback needs no TLS"},
		{"0.0.0.0:8443", false, false, true, "public plain http refused"},
		{":8443", false, false, true, "all interfaces plain http refused"},
		{"192.168.1.5:8443", false, false, true, "lan plain http refused"},
		{"0.0.0.0:8443", true, false, false, "TLS allows any address"},
		{":8443", false, true, false, "behind a proxy allows any address"},
		{"", true, false, true, "empty address refused"},
		{"bad", true, false, true, "address without port refused"},
	}
	for _, c := range cases {
		err := ValidateListen(c.addr, c.tls, c.proxy)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: ValidateListen(%q, tls=%v, proxy=%v) = %v", c.description, c.addr, c.tls, c.proxy, err)
		}
	}
}

func TestOversizedChunkedBodyWithTrailingSpaceIsTooLarge(t *testing.T) {
	h, _ := testHandler(t, nil)
	// A valid object followed by whitespace past the limit: the limit is hit
	// while checking for trailing data, which is still a size problem.
	body := `{"name":"a"}` + strings.Repeat(" ", MaxBodyBytes+10)
	req := httptest.NewRequest("POST", "/api/v1/echo", io.NopCloser(strings.NewReader(body)))
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 413 {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}
