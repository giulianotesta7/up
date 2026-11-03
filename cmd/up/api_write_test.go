package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/remote"
)

// startTestAPI runs `up serve` with the API on a loopback port and returns a
// caller for authenticated requests. It stops the server when the test ends.
func startTestAPI(t *testing.T) func(method, path, body string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "up.db")
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	lines := make(chan string, 64)
	done := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	go func() {
		done <- run(ctx, []string{"serve", "--db", path, "--api-listen", "127.0.0.1:0"}, io.Discard, writer)
		writer.Close()
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("shutdown timed out")
		}
		reader.Close()
	})
	var token, address string
	timeout := time.After(5 * time.Second)
	for address == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("serve stopped")
			}
			if v, found := strings.CutPrefix(line, "up: access token (shown once): "); found {
				token = v
			}
			if v, found := strings.CutPrefix(line, "up: API listening on "); found {
				address = v
			}
		case <-timeout:
			t.Fatal("serve did not listen")
		}
	}
	return func(method, path, body string) (int, string) {
		req, err := http.NewRequest(method, "http://"+address+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(data)
	}
}

func TestAPIWriteLifecycleAndRejections(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer site.Close()
	call := startTestAPI(t)
	check := func(name, target string, status int) string {
		return `{"name":"` + name + `","url":"` + target + `","interval_ms":3600000,"timeout_ms":1000,"expected_status":` + strconv.Itoa(status) + `}`
	}
	snapshot := func() remote.SnapshotDTO {
		code, body := call("GET", "/api/v1/snapshot", "")
		var d remote.SnapshotDTO
		if code != 200 || json.Unmarshal([]byte(body), &d) != nil {
			t.Fatalf("snapshot: %d %s", code, body)
		}
		return d
	}
	find := func(d remote.SnapshotDTO, name string) (remote.ServiceDTO, bool) {
		for _, s := range d.Services {
			if s.Check.Name == name {
				return s, true
			}
		}
		return remote.ServiceDTO{}, false
	}
	name := "my site/é"
	escaped := url.PathEscape(name)

	if code, body := call("POST", "/api/v1/checks", check(name, site.URL, 200)); code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	if s, ok := find(snapshot(), name); !ok || s.Check.URL != site.URL || s.Check.IntervalMS != 3600000 {
		t.Fatalf("created check not in snapshot: %+v %v", s, ok)
	}
	if code, body := call("PUT", "/api/v1/checks/"+escaped, check(name, site.URL+"/health", 204)); code != 204 {
		t.Fatalf("update: %d %s", code, body)
	}
	if s, _ := find(snapshot(), name); s.Check.URL != site.URL+"/health" || s.Check.ExpectedStatus != 204 {
		t.Fatalf("update not applied: %+v", s.Check)
	}
	if code, body := call("PUT", "/api/v1/checks/"+escaped+"/mute", `{"duration_ms":3600000,"was_alerting":false}`); code != 204 {
		t.Fatalf("mute: %d %s", code, body)
	}
	d := snapshot()
	if s, _ := find(d, name); !s.HasMute || s.Mute.Until.Before(d.ServerTime.Add(50*time.Minute)) {
		t.Fatalf("mute not applied: %+v at %v", s.Mute, d.ServerTime)
	}
	if code, body := call("DELETE", "/api/v1/checks/"+escaped+"/mute", ""); code != 204 {
		t.Fatalf("unmute: %d %s", code, body)
	}
	d = snapshot()
	if s, _ := find(d, name); s.Mute.Until.After(d.ServerTime) {
		t.Fatalf("mute still active after unmute: %+v", s.Mute)
	}

	// Rejected requests change nothing.
	if code, _ := call("POST", "/api/v1/checks", check("keep", site.URL, 200)); code != 201 {
		t.Fatal("setup")
	}
	state := func() string {
		d := snapshot()
		var parts []any
		for _, s := range d.Services {
			parts = append(parts, s.Check, s.HasMute, s.Mute)
		}
		out, _ := json.Marshal([]any{parts, d.SMTPServers, d.Channels})
		return string(out)
	}
	before := state()
	for _, tc := range []struct {
		label, method, path, body string
		status                    int
		message                   string
	}{
		{"invalid url", "POST", "/api/v1/checks", check("bad", "not a url", 200), 400, ""},
		{"duplicate", "POST", "/api/v1/checks", check("keep", site.URL, 200), 409, `store: add check: name "keep" already exists`},
		{"update unknown", "PUT", "/api/v1/checks/ghost", check("ghost", site.URL, 200), 404, `store: update check: name "ghost" not found`},
		{"delete unknown", "DELETE", "/api/v1/checks/ghost", "", 404, `store: delete check: name "ghost" not found`},
		{"mute unknown", "PUT", "/api/v1/checks/ghost/mute", `{"duration_ms":60000}`, 404, `store: set mute: name "ghost" not found`},
		{"unknown channel", "PUT", "/api/v1/checks/keep", strings.Replace(check("keep", site.URL, 200), `"expected_status"`, `"channel":"nope","expected_status"`, 1), 400, `channel "nope" does not exist`},
		{"bad duration", "PUT", "/api/v1/checks/keep/mute", `{"duration_ms":1}`, 400, "duration must be between 1m and 7d"},
		{"name mismatch", "PUT", "/api/v1/checks/keep", check("other", site.URL, 200), 400, "name must match path"},
	} {
		code, body := call(tc.method, tc.path, tc.body)
		if code != tc.status {
			t.Errorf("%s: status %d, want %d (%s)", tc.label, code, tc.status, body)
		}
		var e struct{ Error string }
		_ = json.Unmarshal([]byte(body), &e)
		if e.Error == "" || (tc.message != "" && !strings.Contains(e.Error, tc.message)) {
			t.Errorf("%s: error %q, want it to contain %q", tc.label, e.Error, tc.message)
		}
		if strings.Contains(body, "sql") || strings.Contains(body, "constraint") {
			t.Errorf("%s: leaks internals: %s", tc.label, body)
		}
	}
	if after := state(); after != before {
		t.Errorf("rejected requests changed the stored state:\nbefore %s\nafter  %s", before, after)
	}

	if code, body := call("DELETE", "/api/v1/checks/"+escaped, ""); code != 204 {
		t.Fatalf("delete: %d %s", code, body)
	}
	if _, ok := find(snapshot(), name); ok {
		t.Fatal("deleted check still in snapshot")
	}
	if _, ok := find(snapshot(), "keep"); !ok {
		t.Fatal("deleting one check removed another")
	}
}
