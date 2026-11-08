package monitor

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heohlee/up/internal/alert"
	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
)

// hookPayload is the part of a webhook body these tests read.
type hookPayload struct {
	Version int    `json:"version"`
	Event   string `json:"event"`
	Monitor struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"monitor"`
}

// tcpWebhookRig runs the real daemon (monitor loop, notifier, store, check
// package) for one TCP monitor that alerts a webhook channel served by an
// httptest receiver. The target is down until reachable is set; down means a
// real refused connection to a port that was bound and closed.
type tcpWebhookRig struct {
	t         *testing.T
	clock     *fakeClock
	reachable atomic.Bool
	hooks     chan hookPayload
	cancel    context.CancelFunc
	done      chan error
	s         interface {
		Recent(context.Context, string, int) ([]check.Result, error)
	}
}

func startTCPWebhookRig(t *testing.T, windows ...config.MaintenanceWindow) *tcpWebhookRig {
	t.Helper()
	ctx := context.Background()
	s := openStore(t)
	key, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "up.db.key"))
	if err != nil {
		t.Fatal(err)
	}
	r := &tcpWebhookRig{t: t, clock: &fakeClock{}, hooks: make(chan hookPayload, 16), done: make(chan error, 1), s: s}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var p hookPayload
		if req.Method == http.MethodPost && json.NewDecoder(req.Body).Decode(&p) == nil {
			r.hooks <- p
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	sealed, err := secret.Seal(key.PublicKey(), srv.URL+"/hook")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, config.Channel{Name: "hook", Kind: config.KindWebhook, WebhookOrigin: srv.URL, WebhookURL: sealed}); err != nil {
		t.Fatal(err)
	}

	// A closed port: nothing listens, so a real dial is refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	c := config.Check{Name: "db", Kind: config.CheckTCP, Host: "127.0.0.1", Port: port, Interval: 20 * time.Millisecond,
		Timeout: 10 * time.Millisecond, Channel: "hook", AlertAfter: 1, RecoverAfter: 1}
	if err := s.AddCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, w := range windows {
		w.Check = "db"
		if _, err := s.AddMaintenanceWindow(ctx, w); err != nil {
			t.Fatal(err)
		}
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier := alert.NewNotifier(alert.Options{Store: s, Key: key, Logger: quiet})
	checker := check.New()
	checker.Now = r.clock.Now
	checker.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if r.reachable.Load() {
			client, server := net.Pipe()
			server.Close()
			return client, nil
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	go notifier.Run(runCtx)
	go func() {
		r.done <- Run(runCtx, Options{Store: s, Runner: checker, Alerter: notifier, Now: r.clock.Now, Logger: quiet,
			HeartbeatInterval: time.Hour, DiscoveryInterval: 20 * time.Millisecond})
	}()
	t.Cleanup(func() { cancel(); <-r.done })
	return r
}

// next waits for one webhook body.
func (r *tcpWebhookRig) next(what string) hookPayload {
	r.t.Helper()
	select {
	case p := <-r.hooks:
		return p
	case <-time.After(5 * time.Second):
		r.t.Fatalf("timed out waiting for %s", what)
	}
	return hookPayload{}
}

// none asserts nothing is POSTed within a short bounded wait.
func (r *tcpWebhookRig) none(what string) {
	r.t.Helper()
	select {
	case p := <-r.hooks:
		r.t.Fatalf("%s: unexpected webhook %+v", what, p)
	case <-time.After(300 * time.Millisecond):
	}
}

func (r *tcpWebhookRig) requireResult(status check.Status) {
	r.t.Helper()
	waitFor(r.t, "a "+string(status)+" result", func() bool {
		rows, _ := r.s.Recent(context.Background(), "db", 1)
		return len(rows) == 1 && rows[0].Status == status
	})
}

func requireHook(t *testing.T, p hookPayload, event string) {
	t.Helper()
	if p.Version != 1 || p.Event != event || p.Monitor.Name != "db" || p.Monitor.Type != "tcp" {
		t.Fatalf("webhook = %+v, want version 1 %s for tcp monitor db", p, event)
	}
}

func TestTCPMonitorAlertsWebhookDownAndRecovered(t *testing.T) {
	r := startTCPWebhookRig(t)
	requireHook(t, r.next("monitor.down"), "monitor.down")
	r.reachable.Store(true)
	requireHook(t, r.next("monitor.recovered"), "monitor.recovered")
	r.none("steady up")
}

func TestMaintenanceWindowHoldsTCPWebhookThenSendsOwedDownOnce(t *testing.T) {
	window := config.MaintenanceWindow{Weekdays: []time.Weekday{time.Monday}, StartMinute: 10 * 60, Duration: 2 * time.Hour, Zone: "UTC"}
	r := startTCPWebhookRig(t, window)
	r.clock.set(10, 30) // inside the window (Monday 10:00-12:00)
	r.requireResult(check.StatusDown)
	r.none("alert during the window")

	r.clock.set(12, 0) // the end is exclusive: the window is over
	requireHook(t, r.next("the owed down alert"), "monitor.down")
	r.none("duplicate down after the window")
	if rows, _ := r.s.Recent(context.Background(), "db", 5); len(rows) == 0 {
		t.Fatal("results must keep being recorded during the window")
	}
}
