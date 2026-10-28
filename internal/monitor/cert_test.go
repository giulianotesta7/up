package monitor

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

type certRunner struct{ result check.Result }

func (r certRunner) Run(ctx context.Context, c config.Check) check.Result {
	if ctx.Err() != nil {
		return check.Result{Status: check.StatusCanceled}
	}
	return r.result
}

type certRecorder struct {
	recordingAlerter
	muCert     sync.Mutex
	thresholds []int
}

func (a *certRecorder) NotifyCert(_ config.Check, cert config.Cert, _ time.Duration) {
	a.muCert.Lock()
	defer a.muCert.Unlock()
	a.thresholds = append(a.thresholds, cert.Notified)
}
func (a *certRecorder) got() []int {
	a.muCert.Lock()
	defer a.muCert.Unlock()
	return append([]int(nil), a.thresholds...)
}
func runCert(t *testing.T, s *store.Store, a Alerter, r check.Result) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Store: s, Runner: certRunner{r}, Alerter: a, HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	waitFor(t, "certificate observation", func() bool {
		if r.Cert != nil {
			c, ok, _ := s.Cert(context.Background(), r.CheckName)
			return ok && c.Observed.Equal(r.At)
		}
		x, ok, _ := s.Latest(context.Background(), r.CheckName)
		return ok && x.At.Equal(r.At)
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestCertThresholdsRestartAndRenewal(t *testing.T) {
	s := openStore(t)
	alertingCheck(t, s, 1, 1)
	a := &certRecorder{}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	expiry := at.Add(20 * 24 * time.Hour)
	sequence := time.Duration(0)
	feed := func(day int, serial string, end time.Time) {
		sequence++
		runCert(t, s, a, check.Result{CheckName: "api", At: at.Add(time.Duration(day)*24*time.Hour + sequence*time.Nanosecond), Status: check.StatusUp, Cert: &check.CertInfo{Host: "host", Issuer: "ca", Serial: serial, NotAfter: end}})
	}
	feed(6, "a", expiry)
	feed(7, "a", expiry)
	feed(13, "a", expiry)
	feed(13, "a", expiry)
	feed(17, "a", expiry)
	if got := a.got(); !reflect.DeepEqual(got, []int{14, 7, 3}) {
		t.Fatalf("thresholds %v", got)
	}
	feed(18, "b", expiry.Add(60*24*time.Hour))
	c, _, _ := s.Cert(context.Background(), "api")
	if c.Notified != 0 || c.Serial != "b" {
		t.Fatalf("renewed cert %+v", c)
	}
	feed(66, "b", expiry.Add(60*24*time.Hour))
	if got := a.got(); !reflect.DeepEqual(got, []int{14, 7, 3, 14}) {
		t.Fatalf("renewal thresholds %v", got)
	}
	feed(67, "b", expiry.Add(120*24*time.Hour))
	c, _, _ = s.Cert(context.Background(), "api")
	if c.Notified != 0 || len(a.got()) != 4 {
		t.Fatalf("expiry-only renewal: %+v, alerts %v", c, a.got())
	}
}
func TestCertFirstSeenMuteNoChannelAndPlainAlerter(t *testing.T) {
	s := openStore(t)
	c := alertingCheck(t, s, 1, 1)
	a := &certRecorder{}
	now := time.Now().UTC()
	r := check.Result{CheckName: c.Name, At: now, Status: check.StatusUp, Cert: &check.CertInfo{Host: "host", Serial: "a", NotAfter: now.Add(48 * time.Hour)}}
	if err := s.SetMute(context.Background(), c.Name, time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	runCert(t, s, a, r)
	saved, _, _ := s.Cert(context.Background(), c.Name)
	if saved.Notified != 0 || len(a.got()) != 0 {
		t.Fatalf("muted %+v %v", saved, a.got())
	}
	if err := s.Unmute(context.Background(), c.Name, time.Now()); err != nil {
		t.Fatal(err)
	}
	r.At = r.At.Add(time.Second)
	runCert(t, s, a, r)
	if got := a.got(); !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("first seen thresholds %v", got)
	}
	before, _, _ := s.Cert(context.Background(), c.Name)
	r.Cert = nil
	r.At = r.At.Add(time.Second)
	runCert(t, s, a, r)
	after, _, _ := s.Cert(context.Background(), c.Name)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("no-cert changed row: %+v %+v", before, after)
	}
	c.Channel = ""
	if err := s.UpdateCheck(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	r.Cert = &check.CertInfo{Serial: "b", NotAfter: now.Add(48 * time.Hour)}
	r.At = r.At.Add(time.Second)
	runCert(t, s, a, r)
	after, _, _ = s.Cert(context.Background(), c.Name)
	if after.Notified != 0 || len(a.got()) != 1 {
		t.Fatalf("no channel %+v %v", after, a.got())
	}
	c.Channel = "mail"
	if err := s.UpdateCheck(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	r.At = r.At.Add(time.Second)
	runCert(t, s, &recordingAlerter{}, r)
	after, _, _ = s.Cert(context.Background(), c.Name)
	if after.Notified != 0 {
		t.Fatalf("plain alerter marked sent: %+v", after)
	}
}

func TestCertTLSOffIgnoresAndForgetsCertificate(t *testing.T) {
	s := openStore(t)
	c := alertingCheck(t, s, 1, 1)
	bg := context.Background()
	a := &certRecorder{}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cert := &check.CertInfo{Host: "host", Issuer: "ca", Serial: "a", NotAfter: at.Add(5 * 24 * time.Hour)}
	runCert(t, s, a, check.Result{CheckName: "api", At: at, Status: check.StatusUp, Cert: cert})
	if got := a.got(); !reflect.DeepEqual(got, []int{7}) {
		t.Fatalf("thresholds with monitoring on: %v", got)
	}

	c.TLSOff = true
	if err := s.UpdateCheck(bg, c); err != nil {
		t.Fatal(err)
	}
	later := at.Add(time.Hour)
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Store: s, Runner: certRunner{check.Result{CheckName: "api", At: later, Status: check.StatusUp, Cert: &check.CertInfo{Host: "host", Issuer: "ca", Serial: "b", NotAfter: at.Add(2 * 24 * time.Hour)}}}, Alerter: a, HeartbeatInterval: time.Hour, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	}()
	waitFor(t, "result", func() bool { x, ok, _ := s.Latest(bg, "api"); return ok && x.At.Equal(later) })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Cert(bg, "api"); ok {
		t.Fatal("certificate kept after turning TLS monitoring off")
	}
	// A nearer threshold would have warned if monitoring were on.
	if got := a.got(); !reflect.DeepEqual(got, []int{7}) {
		t.Fatalf("thresholds after turning it off: %v", got)
	}
}
