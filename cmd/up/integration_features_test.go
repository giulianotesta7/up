package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

// serveRig runs `up serve` in process over a real temporary store with a
// webhook channel "ops-hook" served by an httptest receiver.
type serveRig struct {
	t     *testing.T
	s     *store.Store
	key   *secret.Key
	hooks chan map[string]any
	auth  chan string
}

func newServeRig(t *testing.T) *serveRig {
	t.Helper()
	isolateDefaultDatabase(t)
	clearAPIEnv(t)
	dbPath := filepath.Join(t.TempDir(), "up.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	key, _, err := secret.LoadOrCreate(dbPath + ".key")
	if err != nil {
		t.Fatal(err)
	}
	r := &serveRig{t: t, s: s, key: key, hooks: make(chan map[string]any, 16), auth: make(chan string, 16)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		if json.NewDecoder(req.Body).Decode(&body) == nil {
			r.auth <- req.Header.Get("X-Hook-Token")
			r.hooks <- body
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	r.addHook(srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var code int
	var errOut string
	go func() {
		defer close(done)
		code, _, errOut = runCmd(ctx, "serve", "--db", dbPath)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if code != 0 {
			t.Errorf("serve exited %d: %s", code, errOut)
		}
	})
	return r
}

func (r *serveRig) seal(value string) secret.Sealed {
	r.t.Helper()
	sealed, err := secret.Seal(r.key.PublicKey(), value)
	if err != nil {
		r.t.Fatal(err)
	}
	return sealed
}

func (r *serveRig) addHook(origin string) {
	r.t.Helper()
	err := r.s.AddChannel(context.Background(), config.Channel{Name: "ops-hook", Kind: config.KindWebhook, WebhookOrigin: origin,
		WebhookURL: r.seal(origin + "/hook"), WebhookAuthName: "X-Hook-Token", WebhookAuth: r.seal("hook-secret")})
	if err != nil {
		r.t.Fatal(err)
	}
}

// waitUntil polls cond with a five-second deadline.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *serveRig) noHook(what string) {
	r.t.Helper()
	select {
	case body := <-r.hooks:
		r.t.Fatalf("%s: unexpected webhook %v", what, body)
	case <-time.After(500 * time.Millisecond):
	}
}

// monitorState is everything a channel test must leave alone.
func monitorState(t *testing.T, s *store.Store, name string) []any {
	t.Helper()
	ctx := context.Background()
	recent, err := s.Recent(ctx, name, 100)
	if err != nil {
		t.Fatal(err)
	}
	mutes, _ := s.Mutes(ctx)
	known, found, _ := s.AlertState(ctx, name)
	outages, _ := s.Outages(ctx, name, 10)
	certs, _ := s.Certs(ctx)
	checks, _ := s.Checks(ctx)
	return []any{recent, mutes, known, found, outages, certs, checks}
}

func TestServeProcessesWebhookChannelTestWithoutTouchingMonitors(t *testing.T) {
	r := newServeRig(t)
	ctx := context.Background()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer target.Close()
	c := config.Check{Name: "api", URL: target.URL, Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200, Channel: "ops-hook"}
	if err := r.s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := r.s.SetMute(ctx, "api", time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	// The one result of an hour-long interval exists before the snapshot.
	waitUntil(t, "the first result", func() bool { rows, _ := r.s.Recent(ctx, "api", 10); return len(rows) == 1 })
	before := monitorState(t, r.s, "api")

	queued, err := r.s.EnqueueChannelTest(ctx, "ops-hook", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	select {
	case body = <-r.hooks:
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon never sent the test")
	}
	if body["event"] != "test" || body["channel"] != "ops-hook" || body["version"] != float64(1) {
		t.Fatalf("body = %v, want a version 1 test event for ops-hook", body)
	}
	if _, has := body["monitor"]; has {
		t.Fatalf("a test names no monitor: %v", body)
	}
	if got := <-r.auth; got != "hook-secret" {
		t.Fatalf("auth header = %q", got)
	}
	var latest config.ChannelTest
	waitUntil(t, "the test to finish", func() bool {
		latest, _, _ = r.s.LatestChannelTest(ctx, "ops-hook")
		return latest.Status == config.TestOK
	})
	if latest.ID != queued.ID || latest.Error != "" {
		t.Fatalf("latest = %+v, want ok for request %d", latest, queued.ID)
	}
	if after := monitorState(t, r.s, "api"); !reflect.DeepEqual(before, after) {
		t.Fatalf("a channel test changed monitor state:\nbefore %+v\nafter  %+v", before, after)
	}
}

func TestServeAdvancedHTTPMonitorRecordsMissingTextAndHonorsMaintenance(t *testing.T) {
	r := newServeRig(t)
	ctx := context.Background()
	type seen struct{ method, header, body string }
	requests := make(chan seen, 16)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		requests <- seen{req.Method, req.Header.Get("X-Api-Key"), string(raw)}
		_, _ = w.Write([]byte("service is degraded"))
	}))
	defer target.Close()
	c := config.Check{Name: "adv", URL: target.URL, Method: "POST", Interval: time.Hour, Timeout: 2 * time.Second, ExpectedStatus: 200,
		Headers:    []config.Header{{Name: "X-Api-Key", Value: r.seal("k3y")}},
		Body:       r.seal(`{"ping":true}`),
		ExpectText: "all systems ready", Channel: "ops-hook", AlertAfter: 1, RecoverAfter: 1}
	if err := r.s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	// Every day all day (back-to-back windows): a window is always active.
	w := config.MaintenanceWindow{Check: "adv", Weekdays: []time.Weekday{0, 1, 2, 3, 4, 5, 6}, StartMinute: 0, Duration: 24 * time.Hour, Zone: "UTC"}
	if _, err := r.s.AddMaintenanceWindow(ctx, w); err != nil {
		t.Fatal(err)
	}

	var got seen
	select {
	case got = <-requests:
	case <-time.After(10 * time.Second):
		t.Fatal("the monitor never made its request")
	}
	if got.method != "POST" || got.header != "k3y" || got.body != `{"ping":true}` {
		t.Fatalf("request = %+v, want POST with the sealed header and body", got)
	}
	var down check.Result
	waitUntil(t, "the down result", func() bool {
		rows, _ := r.s.Recent(ctx, "adv", 1)
		if len(rows) == 1 {
			down = rows[0]
		}
		return len(rows) == 1
	})
	if down.Status != check.StatusDown || down.StatusCode != 200 || !strings.Contains(down.Error, "expected text not found") {
		t.Fatalf("result = %+v, want down for the missing text with status 200", down)
	}
	r.noHook("alert during the maintenance window")
}
