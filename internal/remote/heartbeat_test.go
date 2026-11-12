package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
)

var pingTime = time.Date(2030, 1, 7, 2, 14, 0, 0, time.UTC)

// beatStore is a real store with the daemon key and heartbeats named names.
func beatStore(t *testing.T, names ...string) (*store.Store, *secret.Key) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "up.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	key, _, err := secret.LoadOrCreate(filepath.Join(dir, "up.db.key"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.SetPublicKey(ctx, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := s.AddCheck(ctx, config.Check{Name: name, Kind: config.CheckHeartbeat, Period: time.Hour, Grace: time.Minute}); err != nil {
			t.Fatal(err)
		}
	}
	return s, key
}

func beatToken(t *testing.T, s *store.Store, key *secret.Key, name string) string {
	t.Helper()
	sealed, err := s.HeartbeatToken(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	token, err := key.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// pingFrom sends one request from the client address ip.
func pingFrom(h http.Handler, method, path, ip, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = ip + ":40000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func fixedPingClock() time.Time { return pingTime }

func beatResults(t *testing.T, s *store.Store, name string) []check.Result {
	t.Helper()
	got, err := s.Recent(context.Background(), name, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// response is everything a caller can observe.
type response struct {
	Code   int
	Header http.Header
	Body   string
}

func observe(rec *httptest.ResponseRecorder) response {
	return response{rec.Code, rec.Header(), rec.Body.String()}
}

func TestHeartbeatPingAndFail(t *testing.T) {
	s, key := beatStore(t, "backup")
	h := HeartbeatRoutes(s, PingOptions{Limits: DefaultPingLimits, Now: fixedPingClock})
	token := beatToken(t, s, key, "backup")

	rec := pingFrom(h, "POST", "/api/v1/heartbeat/"+token, "192.0.2.1", "")
	if got := observe(rec); got.Code != 200 || got.Body != "{\"ok\":true}\n" || got.Header.Get("Content-Type") != "application/json" ||
		got.Header.Get("Cache-Control") != "no-store" || got.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("ping = %+v", got)
	}
	// A small body (curl --data) is accepted and ignored.
	if rec := pingFrom(h, "POST", "/api/v1/heartbeat/"+token+"/fail", "192.0.2.1", "exit 3"); rec.Code != 200 || rec.Body.String() != "{\"ok\":true}\n" {
		t.Fatalf("fail = %d %s", rec.Code, rec.Body)
	}
	// The clock stands still, so the failure is stamped just after the ping.
	want := []check.Result{check.Fail("backup", pingTime.Add(time.Nanosecond)), check.Ping("backup", pingTime)}
	if got := beatResults(t, s, "backup"); !reflect.DeepEqual(got, want) {
		t.Fatalf("results = %+v, want %+v", got, want)
	}
}

func TestHeartbeatUnknownAndWrongTokensLookTheSame(t *testing.T) {
	s, key := beatStore(t, "backup")
	h := HeartbeatRoutes(s, PingOptions{Limits: DefaultPingLimits, Now: fixedPingClock})
	token := beatToken(t, s, key, "backup")
	regenerated, err := s.RegenerateHeartbeatToken(context.Background(), "backup")
	if err != nil {
		t.Fatal(err)
	}
	notFound := response{404, http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}, "{\"error\":\"not found\"}\n"}
	for i, path := range []string{
		"/api/v1/heartbeat/" + token,                        // a former token
		"/api/v1/heartbeat/" + token + "/fail",              // its failure URL
		"/api/v1/heartbeat/uphb_" + strings.Repeat("A", 43), // well formed, unknown
		"/api/v1/heartbeat/x",                               // malformed
		"/api/v1/heartbeat/",                                // no token
		"/api/v1/heartbeat/" + regenerated + "/other",       // the valid token, another action
		"/api/v1/heartbeat/" + regenerated + "/fail/extra",  // too deep
		"/api/v1/heartbeat/%2e%2e",                          // escaped dots
		"/api/v1/heartbeat/" + strings.Repeat("A", 4096),    // long
	} {
		// Distinct addresses keep this about responses, not rate limits.
		got := observe(pingFrom(h, "POST", path, fmt.Sprintf("192.0.2.%d", i+1), ""))
		if !reflect.DeepEqual(got, notFound) {
			t.Errorf("POST %s = %+v, want %+v", path, got, notFound)
		}
	}
	if got := beatResults(t, s, "backup"); len(got) != 0 {
		t.Fatalf("results = %+v, want none", got)
	}
}

func TestHeartbeatMethodsAndBodySizeDoNotDependOnTheToken(t *testing.T) {
	s, key := beatStore(t, "backup")
	h := HeartbeatRoutes(s, PingOptions{Limits: DefaultPingLimits, Now: fixedPingClock})
	token := beatToken(t, s, key, "backup")
	for i, method := range []string{"GET", "HEAD", "PUT", "DELETE"} {
		valid := observe(pingFrom(h, method, "/api/v1/heartbeat/"+token, fmt.Sprintf("192.0.2.%d", i+1), ""))
		unknown := observe(pingFrom(h, method, "/api/v1/heartbeat/nope", fmt.Sprintf("198.51.100.%d", i+1), ""))
		if valid.Code != 405 || valid.Header.Get("Allow") != "POST" || !reflect.DeepEqual(valid, unknown) {
			t.Errorf("%s = %+v, unknown %+v", method, valid, unknown)
		}
	}
	big := strings.Repeat("x", MaxPingBody+1)
	valid := observe(pingFrom(h, "POST", "/api/v1/heartbeat/"+token, "203.0.113.1", big))
	unknown := observe(pingFrom(h, "POST", "/api/v1/heartbeat/nope", "203.0.113.2", big))
	if valid.Code != 413 || valid.Body != "{\"error\":\"request body too large\"}\n" || !reflect.DeepEqual(valid, unknown) {
		t.Fatalf("large body = %+v, unknown %+v", valid, unknown)
	}
	if got := beatResults(t, s, "backup"); len(got) != 0 {
		t.Fatalf("results = %+v, want none", got)
	}
}

func TestHeartbeatRateLimits(t *testing.T) {
	names := make([]string, 30)
	for i := range names {
		names[i] = fmt.Sprintf("job-%02d", i)
	}
	s, key := beatStore(t, names...)
	h := HeartbeatRoutes(s, PingOptions{Limits: DefaultPingLimits, Now: fixedPingClock})
	tooMany := response{429, http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}, "Retry-After": {"1"}}, "{\"error\":\"too many requests\"}\n"}

	// 30 jobs behind one proxy fire at the same minute: all are recorded.
	var wg sync.WaitGroup
	codes := make([]int, len(names))
	for i, name := range names {
		token := beatToken(t, s, key, name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = pingFrom(h, "POST", "/api/v1/heartbeat/"+token, "10.0.0.1", "").Code
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != 200 {
			t.Fatalf("job %d = %d, want every job recorded", i, code)
		}
	}

	// One token: a burst of 5, then 429; an unknown token is limited the
	// same way, so a 429 says nothing about which tokens exist.
	token := beatToken(t, s, key, "job-00")
	for i := range 4 {
		if code := pingFrom(h, "POST", "/api/v1/heartbeat/"+token, "10.0.0.2", "").Code; code != 200 {
			t.Fatalf("ping %d = %d", i, code)
		}
	}
	if got := observe(pingFrom(h, "POST", "/api/v1/heartbeat/"+token, "10.0.0.2", "")); !reflect.DeepEqual(got, tooMany) {
		t.Fatalf("sixth ping of one token = %+v, want %+v", got, tooMany)
	}
	for i := range 5 {
		if code := pingFrom(h, "POST", "/api/v1/heartbeat/unknown", "10.0.0.3", "").Code; code != 404 {
			t.Fatalf("unknown %d = %d", i, code)
		}
	}
	if got := observe(pingFrom(h, "POST", "/api/v1/heartbeat/unknown", "10.0.0.3", "")); !reflect.DeepEqual(got, tooMany) {
		t.Fatalf("sixth request of one unknown token = %+v", got)
	}
	if got := beatResults(t, s, "job-00"); len(got) != 5 {
		t.Fatalf("job-00 results = %d, want 5 (the limited request records nothing)", len(got))
	}
}

func TestHeartbeatUnknownTokensCannotBlockValidPings(t *testing.T) {
	s, key := beatStore(t, "backup")
	h := HeartbeatRoutes(s, PingOptions{Limits: DefaultPingLimits, Now: fixedPingClock})
	token := beatToken(t, s, key, "backup")
	// 4 addresses send 100 distinct unknown tokens each: every address
	// spends its burst and together they drain the unknown-token bucket.
	for a := range 4 {
		for i := range 100 {
			if code := pingFrom(h, "POST", fmt.Sprintf("/api/v1/heartbeat/uphb_%d_%d", a, i), fmt.Sprintf("10.0.0.%d", a+1), "").Code; code != 404 && code != 429 {
				t.Fatalf("unknown %d from address %d = %d", i, a, code)
			}
		}
	}
	if code := pingFrom(h, "POST", "/api/v1/heartbeat/uphb_more", "10.0.9.1", "").Code; code != 429 {
		t.Fatalf("unknown token from a fresh address after the flood = %d, want 429", code)
	}
	// A valid token is not limited by them, from a fresh or a flooding address.
	for _, ip := range []string{"10.0.9.2", "10.0.0.1"} {
		if code := pingFrom(h, "POST", "/api/v1/heartbeat/"+token, ip, "").Code; code != 200 {
			t.Fatalf("valid token from %s after the flood = %d, want 200", ip, code)
		}
	}
	if got := beatResults(t, s, "backup"); len(got) != 2 {
		t.Fatalf("results = %d, want 2", len(got))
	}
}

func TestHeartbeatRateLimitsUnknownTokensPerAddressAndGlobally(t *testing.T) {
	s, key := beatStore(t, "backup")
	h := HeartbeatRoutes(s, PingOptions{Limits: PingLimits{TokenRate: 1, TokenBurst: 5, IPRate: 1, IPBurst: 3, UnknownRate: 1, UnknownBurst: 5, CapRate: 100, CapBurst: 100}, Now: fixedPingClock})
	token := beatToken(t, s, key, "backup")
	for i := range 3 {
		if code := pingFrom(h, "POST", fmt.Sprintf("/api/v1/heartbeat/t%d", i), "10.0.0.1", "").Code; code != 404 {
			t.Fatalf("request %d = %d", i, code)
		}
	}
	if code := pingFrom(h, "POST", "/api/v1/heartbeat/t9", "10.0.0.1", "").Code; code != 429 {
		t.Fatalf("fourth unknown token of one address = %d, want 429", code)
	}
	for i := range 2 {
		if code := pingFrom(h, "POST", "/api/v1/heartbeat/u", fmt.Sprintf("10.0.1.%d", i), "").Code; code != 404 {
			t.Fatalf("other address %d = %d", i, code)
		}
	}
	if code := pingFrom(h, "POST", "/api/v1/heartbeat/v", "10.0.2.1", "").Code; code != 429 {
		t.Fatalf("unknown token over the unknown-token limit = %d, want 429", code)
	}
	if code := pingFrom(h, "POST", "/api/v1/heartbeat/"+token, "10.0.0.1", "").Code; code != 200 {
		t.Fatalf("valid token from the limited address = %d, want 200", code)
	}
}

func TestHeartbeatSafetyCapLimitsEveryToken(t *testing.T) {
	names := []string{"a", "b", "c"}
	s, key := beatStore(t, names...)
	h := HeartbeatRoutes(s, PingOptions{Limits: PingLimits{TokenRate: 1, TokenBurst: 5, IPRate: 20, IPBurst: 100, UnknownRate: 50, UnknownBurst: 200, CapRate: 1, CapBurst: 2}, Now: fixedPingClock})
	var codes []int
	for _, name := range names {
		codes = append(codes, pingFrom(h, "POST", "/api/v1/heartbeat/"+beatToken(t, s, key, name), "10.0.0.1", "").Code)
	}
	if want := []int{200, 200, 429}; !reflect.DeepEqual(codes, want) {
		t.Fatalf("codes = %v, want %v", codes, want)
	}
	// Methods and body sizes are refused before the cap and do not spend it.
	if code := pingFrom(h, "GET", "/api/v1/heartbeat/x", "10.0.0.1", "").Code; code != 405 {
		t.Fatalf("GET over the cap = %d, want 405", code)
	}
}

// bodyClock is a clock that moves an hour once the request body was read.
type bodyClock struct {
	mu   sync.Mutex
	read bool
}

func (c *bodyClock) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.read = true
	return 0, io.EOF
}

func (c *bodyClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.read {
		return pingTime.Add(time.Hour)
	}
	return pingTime
}

func TestHeartbeatPingIsStampedAfterTheBodyIsRead(t *testing.T) {
	s, key := beatStore(t, "backup")
	clock := &bodyClock{}
	h := HeartbeatRoutes(s, PingOptions{Limits: DefaultPingLimits, Now: clock.now})
	req := httptest.NewRequest("POST", "/api/v1/heartbeat/"+beatToken(t, s, key, "backup"), clock)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("ping = %d", rec.Code)
	}
	if got := beatResults(t, s, "backup"); len(got) != 1 || got[0] != check.Ping("backup", pingTime.Add(time.Hour)) {
		t.Fatalf("results = %+v, want a ping stamped after the body", got)
	}
}

func TestTokenBucketRefills(t *testing.T) {
	now := pingTime
	l := newLimiter(2, 2, 10)
	later := now.Add(500 * time.Millisecond)
	got := []bool{l.allow("a", now), l.allow("a", now), l.allow("a", now), l.allow("a", later), l.allow("a", later)}
	// A burst of 2, then 2/s refills one in half a second.
	if want := []bool{true, true, false, true, false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("allow = %v, want %v", got, want)
	}
	// A full map forgets idle (full) buckets before refusing new keys.
	for i := range 10 {
		l.allow(fmt.Sprintf("k%d", i), now)
	}
	if !l.allow("new", now.Add(time.Hour)) {
		t.Fatal("idle buckets were not forgotten")
	}
}

type failingPings struct{}

func (failingPings) PingHeartbeat(context.Context, string, bool, time.Time) (bool, error) {
	return false, errors.New("database is locked: uphb_secret")
}

func (failingPings) HeartbeatTokenKnown(context.Context, string) (bool, error) {
	return true, nil
}

func TestHeartbeatStoreErrorsHideTheToken(t *testing.T) {
	h := HeartbeatRoutes(failingPings{}, PingOptions{Limits: DefaultPingLimits, Now: fixedPingClock})
	rec := pingFrom(h, "POST", "/api/v1/heartbeat/uphb_secret", "192.0.2.1", "")
	if rec.Code != 500 || rec.Body.String() != "{\"error\":\"internal error\"}\n" {
		t.Fatalf("store error = %d %s", rec.Code, rec.Body)
	}
}

func tokenServer(t *testing.T) (http.Handler, *store.Store, *secret.Key) {
	t.Helper()
	s, key := beatStore(t, "backup")
	if err := s.AddCheck(context.Background(), config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}); err != nil {
		t.Fatal(err)
	}
	h, _ := testHandler(t, func(m *http.ServeMux) {
		writes := WriteRoutes(s)
		m.Handle("/api/v1/checks", writes)
		m.Handle("/api/v1/checks/", writes)
		m.Handle("/api/v1/checks/{name}/heartbeat-token", HeartbeatTokenRoutes(s, key))
	})
	return h, s, key
}

func TestHeartbeatTokenRoutes(t *testing.T) {
	h, s, key := tokenServer(t)
	token := beatToken(t, s, key, "backup")
	rec := do(h, "GET", "/api/v1/checks/backup/heartbeat-token", nil, bearer(testToken))
	var got HeartbeatTokenDTO
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got != (HeartbeatTokenDTO{Token: token, Path: "/api/v1/heartbeat/" + token}) {
		t.Fatalf("reveal = %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "POST", "/api/v1/checks/backup/heartbeat-token", nil, bearer(testToken))
	var fresh HeartbeatTokenDTO
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &fresh) != nil || fresh.Token == token || fresh.Path != "/api/v1/heartbeat/"+fresh.Token || beatToken(t, s, key, "backup") != fresh.Token {
		t.Fatalf("regenerate = %d %s", rec.Code, rec.Body)
	}
	if found, _ := s.PingHeartbeat(context.Background(), token, false, pingTime); found {
		t.Fatal("the old token still works")
	}
	for _, tc := range []struct {
		method, path string
		headers      map[string]string
		code         int
		body         string
	}{
		{"GET", "/api/v1/checks/backup/heartbeat-token", nil, 401, "{\"error\":\"unauthorized\"}\n"},
		{"POST", "/api/v1/checks/backup/heartbeat-token", nil, 401, "{\"error\":\"unauthorized\"}\n"},
		{"GET", "/api/v1/checks/api/heartbeat-token", bearer(testToken), 404, "{\"error\":\"store: heartbeat token: heartbeat monitor \\\"api\\\" not found\"}\n"},
		{"POST", "/api/v1/checks/missing/heartbeat-token", bearer(testToken), 404, "{\"error\":\"store: regenerate heartbeat token: heartbeat monitor \\\"missing\\\" not found\"}\n"},
		{"DELETE", "/api/v1/checks/backup/heartbeat-token", bearer(testToken), 405, ""},
	} {
		rec := do(h, tc.method, tc.path, nil, tc.headers)
		if rec.Code != tc.code || rec.Body.String() != tc.body {
			t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.path, rec.Code, rec.Body, tc.code, tc.body)
		}
	}
	if got := beatToken(t, s, key, "backup"); got != fresh.Token {
		t.Fatal("a refused request changed the token")
	}
}

func TestHeartbeatTokenSealedToAnotherKey(t *testing.T) {
	s, _ := beatStore(t, "backup")
	other, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	h, _ := testHandler(t, func(m *http.ServeMux) {
		m.Handle("/api/v1/checks/{name}/heartbeat-token", HeartbeatTokenRoutes(s, other))
	})
	rec := do(h, "GET", "/api/v1/checks/backup/heartbeat-token", nil, bearer(testToken))
	if rec.Code != 409 || rec.Body.String() != "{\"error\":\"the token was sealed with another encryption key; replace the token\"}\n" {
		t.Fatalf("reveal with another key = %d %s", rec.Code, rec.Body)
	}
}

func TestHeartbeatCheckThroughTheAPIAndSnapshot(t *testing.T) {
	h, s, key := tokenServer(t)
	body := `{"name":"nightly","kind":"heartbeat","period_ms":86400000,"grace_ms":1800000,"alert_after":2}`
	rec := send(h, "POST", "/api/v1/checks", body)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"kind":"heartbeat"`) || !strings.Contains(rec.Body.String(), `"period_ms":86400000,"grace_ms":1800000`) {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	want := config.Check{Name: "nightly", Kind: config.CheckHeartbeat, Period: 24 * time.Hour, Grace: 30 * time.Minute, AlertAfter: 2}
	if got := storedCheck(t, s, "nightly"); !reflect.DeepEqual(got, want) {
		t.Fatalf("stored %+v, want %+v", got, want)
	}
	for _, bad := range []string{
		`{"name":"x","kind":"heartbeat","period_ms":1000}`,
		`{"name":"x","kind":"heartbeat","period_ms":3600000,"url":"https://example.com"}`,
		`{"name":"x","kind":"heartbeat","period_ms":9223372036854775807}`,
		`{` + plainCheck[len(`"name":"api",`):] + `,"name":"x","grace_ms":1000}`,
	} {
		if rec := send(h, "POST", "/api/v1/checks", bad); rec.Code != 400 {
			t.Errorf("create %s = %d %s, want 400", bad, rec.Code, rec.Body)
		}
	}
	nightly := beatToken(t, s, key, "nightly")
	if found, err := s.PingHeartbeat(context.Background(), nightly, false, pingTime); err != nil || !found {
		t.Fatal(found, err)
	}
	if err := s.SetAPIListener(context.Background(), config.APIListener{Address: "10.0.0.5:8080"}); err != nil {
		t.Fatal(err)
	}
	snap, err := tui.Load(context.Background(), s, pingTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(NewSnapshotDTO(snap, pingTime.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{nightly, beatToken(t, s, key, "backup")} {
		if strings.Contains(string(b), token) {
			t.Fatal("the snapshot JSON carries a ping token")
		}
	}
	for _, part := range []string{`"api":{"listen":"10.0.0.5:8080","tls":false}`, `"heartbeat":{"waiting":false,"last_ping":"2030-01-07T02:14:00Z","deadline":"2030-01-08T02:44:00Z"}`, `"heartbeat":{"waiting":true,`} {
		if !strings.Contains(string(b), part) {
			t.Errorf("snapshot JSON lacks %s:\n%s", part, b)
		}
	}
	var dto SnapshotDTO
	if err := json.Unmarshal(b, &dto); err != nil {
		t.Fatal(err)
	}
	back := dto.Snapshot()
	if back.API != snap.API {
		t.Fatalf("API round trip = %+v, want %+v", back.API, snap.API)
	}
	for i := range snap.Services {
		if back.Services[i].Heartbeat != snap.Services[i].Heartbeat || !reflect.DeepEqual(back.Services[i].Check, snap.Services[i].Check) {
			t.Fatalf("service %d round trip = %+v / %+v, want %+v / %+v", i, back.Services[i].Check, back.Services[i].Heartbeat, snap.Services[i].Check, snap.Services[i].Heartbeat)
		}
	}
	if back.Services[2].Label() != "UP" || back.Services[0].Label() != "WAITING" {
		t.Fatalf("labels %q %q", back.Services[0].Label(), back.Services[2].Label())
	}
}
