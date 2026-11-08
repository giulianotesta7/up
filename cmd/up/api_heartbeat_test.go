package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
)

// daemon is a running `up serve` with its whole stderr.
type daemon struct {
	t       *testing.T
	cancel  context.CancelFunc
	done    chan int
	mu      sync.Mutex
	log     strings.Builder
	address string
	token   string
}

func (d *daemon) stderr() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.log.String()
}

// startDaemon runs serve with args and waits for wait to appear on stderr.
func startDaemon(t *testing.T, wait string, args ...string) *daemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{t: t, cancel: cancel, done: make(chan int, 1)}
	reader, writer := io.Pipe()
	lines := make(chan string, 64)
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			d.mu.Lock()
			d.log.WriteString(scanner.Text() + "\n")
			d.mu.Unlock()
			lines <- scanner.Text()
		}
		close(lines)
	}()
	go func() {
		d.done <- run(ctx, append([]string{"serve"}, args...), io.Discard, writer)
		writer.Close()
	}()
	t.Cleanup(d.stop)
	timeout := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatalf("serve stopped:\n%s", d.stderr())
			}
			if v, ok := strings.CutPrefix(line, "up: access token (shown once): "); ok {
				d.token = v
			}
			if v, ok := strings.CutPrefix(line, "up: API listening on "); ok {
				d.address = v
			}
			if strings.Contains(line, wait) {
				go func() {
					for range lines {
					}
				}()
				return d
			}
		case <-timeout:
			t.Fatalf("serve did not log %q:\n%s", wait, d.stderr())
		}
	}
}

func (d *daemon) stop() {
	if d.cancel == nil {
		return
	}
	d.cancel()
	d.cancel = nil
	select {
	case code := <-d.done:
		if code != 0 {
			d.t.Errorf("serve exit %d:\n%s", code, d.stderr())
		}
	case <-time.After(5 * time.Second):
		d.t.Error("shutdown timed out")
	}
}

func (d *daemon) call(method, path, bearer string) (int, string) {
	d.t.Helper()
	req, err := http.NewRequest(method, "http://"+d.address+path, nil)
	if err != nil {
		d.t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		d.t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

// heartbeatDatabase prepares a database with its key, a heartbeat "backup"
// and an HTTP monitor "site" of target, and returns the backup's token.
func heartbeatDatabase(t *testing.T, path, target string) string {
	t.Helper()
	key, _, err := secret.LoadOrCreate(path + ".key")
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.SetPublicKey(ctx, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCheck(ctx, config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: time.Hour, Grace: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddCheck(ctx, config.Check{Name: "site", URL: target, Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200}); err != nil {
		t.Fatal(err)
	}
	sealed, err := s.HeartbeatToken(ctx, "backup")
	if err != nil {
		t.Fatal(err)
	}
	token, err := key.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func readResults(t *testing.T, path, name string) []check.Result {
	t.Helper()
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.Recent(context.Background(), name, 100)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestServeHeartbeatEndpoint(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer site.Close()
	path := filepath.Join(t.TempDir(), "up.db")
	ping := heartbeatDatabase(t, path, site.URL)
	d := startDaemon(t, "up: API listening on ", "--db", path, "--api-listen", "127.0.0.1:0")

	// The ping token is the only credential: no Authorization header.
	if code, body := d.call("POST", "/api/v1/heartbeat/"+ping, ""); code != 200 || body != "{\"ok\":true}\n" {
		t.Fatalf("ping = %d %q", code, body)
	}
	if code, body := d.call("POST", "/api/v1/heartbeat/"+ping+"/fail", ""); code != 200 || body != "{\"ok\":true}\n" {
		t.Fatalf("fail = %d %q", code, body)
	}
	unknownCode, unknown := d.call("POST", "/api/v1/heartbeat/uphb_"+strings.Repeat("x", 43), "")
	if unknownCode != 404 || unknown != "{\"error\":\"not found\"}\n" {
		t.Fatalf("unknown token = %d %q", unknownCode, unknown)
	}
	if code, _ := d.call("GET", "/api/v1/heartbeat/"+ping, ""); code != 405 {
		t.Fatalf("GET ping = %d, want 405", code)
	}
	// The ping token opens nothing else, and the API still needs its token.
	if code, _ := d.call("GET", "/api/v1/snapshot", ping); code != 401 {
		t.Fatalf("snapshot with the ping token = %d, want 401", code)
	}
	if code, _ := d.call("GET", "/api/v1/checks/backup/heartbeat-token", ""); code != 401 {
		t.Fatalf("reveal without the API token = %d, want 401", code)
	}

	code, body := d.call("GET", "/api/v1/checks/backup/heartbeat-token", d.token)
	var revealed struct{ Token, Path string }
	if code != 200 || json.Unmarshal([]byte(body), &revealed) != nil || revealed.Token != ping || revealed.Path != "/api/v1/heartbeat/"+ping {
		t.Fatalf("reveal = %d %s", code, body)
	}
	code, body = d.call("POST", "/api/v1/checks/backup/heartbeat-token", d.token)
	var fresh struct{ Token string }
	if code != 200 || json.Unmarshal([]byte(body), &fresh) != nil || fresh.Token == ping {
		t.Fatalf("regenerate = %d %s", code, body)
	}
	// The old token is now exactly like an unknown one.
	if code, body := d.call("POST", "/api/v1/heartbeat/"+ping, ""); code != unknownCode || body != unknown {
		t.Fatalf("old token = %d %q, want %d %q", code, body, unknownCode, unknown)
	}
	if code, _ := d.call("POST", "/api/v1/heartbeat/"+fresh.Token, ""); code != 200 {
		t.Fatalf("new token = %d", code)
	}

	code, snapshot := d.call("GET", "/api/v1/snapshot", d.token)
	port := d.address[strings.LastIndex(d.address, ":")+1:]
	if code != 200 || !strings.Contains(snapshot, fmt.Sprintf(`"api":{"listen":"127.0.0.1:%s","tls":false}`, port)) || !strings.Contains(snapshot, `"kind":"heartbeat"`) {
		t.Fatalf("snapshot = %d %s", code, snapshot)
	}
	for _, token := range []string{ping, fresh.Token} {
		if strings.Contains(snapshot, token) {
			t.Fatal("the snapshot carries a ping token")
		}
	}
	// The HTTP monitor beside it is checked as before.
	deadline := time.Now().Add(5 * time.Second)
	for len(readResults(t, path, "site")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the HTTP monitor was not checked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.stop()
	if got := readResults(t, path, "site"); got[0].Status != check.StatusUp || got[0].StatusCode != 200 {
		t.Fatalf("site result = %+v", got[0])
	}
	got := readResults(t, path, "backup")
	if len(got) != 3 || got[0].Status != check.StatusUp || !check.Failed(got[1]) || got[2].Status != check.StatusUp {
		t.Fatalf("backup results = %+v, want ping, failure, ping", got)
	}
	for _, token := range []string{ping, fresh.Token} {
		if strings.Contains(d.stderr(), token) {
			t.Fatalf("the daemon log contains a ping token:\n%s", d.stderr())
		}
	}
}

func TestServeRecordsWhetherItHasAnAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db")
	heartbeatDatabase(t, path, "http://127.0.0.1:1")
	api := func() config.APIListener {
		t.Helper()
		s, err := store.OpenReadOnly(path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		a, err := s.APIListener(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	d := startDaemon(t, "up: API listening on ", "--db", path, "--api-listen", "127.0.0.1:0")
	if got := api(); got != (config.APIListener{Address: d.address}) {
		t.Fatalf("APIListener() with --api-listen = %+v, want %s", got, d.address)
	}
	d.stop()
	d = startDaemon(t, "monitoring started", "--db", path)
	if got := api(); got.Enabled() {
		t.Fatalf("APIListener() without --api-listen = %+v, want none", got)
	}
	d.stop()
}
