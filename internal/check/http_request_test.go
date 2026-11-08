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
	"github.com/heohlee/up/internal/secret"
)

func keyedChecker(t *testing.T) (*Checker, *secret.Key) {
	t.Helper()
	key, _, err := secret.LoadOrCreate(t.TempDir() + "/k")
	if err != nil {
		t.Fatal(err)
	}
	ch := newChecker()
	ch.Key = key
	return ch, key
}

func seal(t *testing.T, key *secret.Key, v string) secret.Sealed {
	t.Helper()
	s, err := secret.Seal(key.PublicKey(), v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRunSendsMethodHeadersAndBody(t *testing.T) {
	type seen struct{ method, auth, ua, body, host string }
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.Header.Get("Authorization"), r.Header.Get("User-Agent"), string(b), r.Host}
	}))
	defer srv.Close()
	ch, key := keyedChecker(t)
	c := checkFor(srv.URL, 200, time.Second)
	c.Method = "POST"
	c.Headers = []config.Header{{Name: "Authorization", Value: seal(t, key, "Bearer s3cret")}, {Name: "Host", Value: seal(t, key, "api.example.test")}}
	c.Body = seal(t, key, `{"probe":true}`)
	res := ch.Run(context.Background(), c)
	if res.Status != StatusUp {
		t.Fatalf("result = %+v, want up", res)
	}
	s := <-got
	if s.method != "POST" || s.auth != "Bearer s3cret" || s.body != `{"probe":true}` || s.host != "api.example.test" || s.ua != userAgent {
		t.Fatalf("server saw %+v", s)
	}
}

func TestRunEmptyMethodIsGET(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Method }))
	defer srv.Close()
	if res := newChecker().Run(context.Background(), checkFor(srv.URL, 200, time.Second)); res.Status != StatusUp {
		t.Fatalf("result = %+v", res)
	}
	if m := <-got; m != "GET" {
		t.Fatalf("method = %q, want GET", m)
	}
}

func TestRunProtectedValuesNeedTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("request must not be sent") }))
	defer srv.Close()
	_, key := keyedChecker(t)
	other, _, _ := secret.LoadOrCreate(t.TempDir() + "/other")
	for name, ch := range map[string]*Checker{"no key": newChecker(), "other key": func() *Checker { c := newChecker(); c.Key = other; return c }()} {
		c := checkFor(srv.URL, 200, time.Second)
		c.Headers = []config.Header{{Name: "X-Token", Value: seal(t, key, "topsecret")}}
		res := ch.Run(context.Background(), c)
		if res.Status != StatusDown || res.Error != "cannot decrypt the request headers or body (encryption key missing or changed)" || strings.Contains(res.Error, "topsecret") {
			t.Fatalf("%s: result = %+v", name, res)
		}
	}
}

func textServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
}

func TestRunExpectText(t *testing.T) {
	srv := textServer("hello Ready world")
	defer srv.Close()
	cases := []struct {
		name, text, status string
		want               Status
		err                string
	}{
		{"found", "Ready", "", StatusUp, ""},
		{"case sensitive", "ready", "", StatusDown, "expected text not found"},
		{"literal not regex", "Re.dy", "", StatusDown, "expected text not found"},
	}
	for _, tc := range cases {
		c := checkFor(srv.URL, 200, time.Second)
		c.ExpectText = tc.text
		res := newChecker().Run(context.Background(), c)
		if res.Status != tc.want || res.Error != tc.err || res.StatusCode != 200 {
			t.Fatalf("%s: result = %+v", tc.name, res)
		}
	}
	// Status and text must both match: a wrong status wins and keeps the old message.
	c := checkFor(srv.URL, 201, time.Second)
	c.ExpectText = "Ready"
	if res := newChecker().Run(context.Background(), c); res.Status != StatusDown || res.Error != "unexpected status 200 (expected 201)" {
		t.Fatalf("status mismatch: %+v", res)
	}
}

func TestRunExpectTextCap(t *testing.T) {
	big := strings.Repeat("a", maxExpectRead) + "needle"
	srv := textServer(big)
	defer srv.Close()
	c := checkFor(srv.URL, 200, 5*time.Second)
	c.ExpectText = "needle"
	res := newChecker().Run(context.Background(), c)
	if res.Status != StatusDown || res.Error != "expected text not found in the first 1 MiB (the response is larger and was not read further)" {
		t.Fatalf("result = %+v", res)
	}
	// Text inside the cap is still found in a larger response.
	srv2 := textServer("needle" + strings.Repeat("a", maxExpectRead))
	defer srv2.Close()
	c.URL = srv2.URL
	if res := newChecker().Run(context.Background(), c); res.Status != StatusUp {
		t.Fatalf("text inside cap: %+v", res)
	}
	// A body of exactly the cap that lacks the text is a plain miss.
	srv3 := textServer(strings.Repeat("a", maxExpectRead))
	defer srv3.Close()
	c.URL = srv3.URL
	if res := newChecker().Run(context.Background(), c); res.Error != "expected text not found" {
		t.Fatalf("exact cap: %+v", res)
	}
}

func TestRunExpectTextBodyReadIsInsideTimeout(t *testing.T) {
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
	c.ExpectText = "x"
	res := newChecker().Run(context.Background(), c)
	if res.Status != StatusDown || res.Error != "timeout after 150ms" || res.Latency == 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunPlainMonitorUnchanged(t *testing.T) {
	var gotMethod, gotUA, gotCT string
	var gotLen int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotUA, gotCT, gotLen = r.Method, r.Header.Get("User-Agent"), r.Header.Get("Content-Type"), r.ContentLength
		_, _ = io.WriteString(w, "anything")
	}))
	defer srv.Close()
	res := newChecker().Run(context.Background(), checkFor(srv.URL, 200, time.Second))
	if res.Status != StatusUp || res.Error != "" || res.StatusCode != 200 || gotMethod != "GET" || gotUA != userAgent || gotCT != "" || gotLen != 0 {
		t.Fatalf("result = %+v method=%q ua=%q ct=%q len=%d", res, gotMethod, gotUA, gotCT, gotLen)
	}
}

// redirectPair returns an origin that redirects to a target on another host
// name (localhost vs 127.0.0.1) and records what the target receives.
func redirectPair(t *testing.T, code int, sameOrigin bool) (origin *httptest.Server, received chan http.Header, bodies chan string) {
	t.Helper()
	received, bodies = make(chan http.Header, 4), make(chan string, 4)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- r.Header.Clone()
		bodies <- string(b)
	}))
	t.Cleanup(target.Close)
	origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			b, _ := io.ReadAll(r.Body)
			received <- r.Header.Clone()
			bodies <- string(b)
			return
		}
		dest := strings.Replace(target.URL, "127.0.0.1", "localhost", 1)
		if sameOrigin {
			dest = "/final"
		}
		http.Redirect(w, r, dest, code)
	}))
	t.Cleanup(origin.Close)
	return
}

func TestRunDoesNotFollowCrossHostRedirectWithProtectedValues(t *testing.T) {
	for _, code := range []int{302, 307} {
		origin, received, _ := redirectPair(t, code, false)
		ch, key := keyedChecker(t)
		c := checkFor(origin.URL, 200, time.Second)
		c.Method = "POST"
		c.Headers = []config.Header{{Name: "X-Token", Value: seal(t, key, "s3cret")}}
		c.Body = seal(t, key, "payload")
		res := ch.Run(context.Background(), c)
		if res.Status != StatusDown || res.Error != "redirect to another host not followed: the request carries protected values" {
			t.Fatalf("%d: result = %+v", code, res)
		}
		select {
		case h := <-received:
			t.Fatalf("%d: target received %v", code, h)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestRunFollowsSameOriginRedirectWithProtectedValues(t *testing.T) {
	origin, received, bodies := redirectPair(t, 307, true)
	ch, key := keyedChecker(t)
	c := checkFor(origin.URL, 200, time.Second)
	c.Method = "POST"
	c.Headers = []config.Header{{Name: "X-Token", Value: seal(t, key, "s3cret")}}
	c.Body = seal(t, key, "payload")
	res := ch.Run(context.Background(), c)
	if res.Status != StatusUp || (<-received).Get("X-Token") != "s3cret" || <-bodies != "payload" {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunPlainMonitorStillFollowsCrossHostRedirect(t *testing.T) {
	origin, received, _ := redirectPair(t, 302, false)
	res := newChecker().Run(context.Background(), checkFor(origin.URL, 200, time.Second))
	if res.Status != StatusUp {
		t.Fatalf("result = %+v", res)
	}
	<-received
}
