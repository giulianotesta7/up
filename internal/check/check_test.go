package check

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

var fixedNow = time.Date(2026, 10, 25, 3, 12, 0, 0, time.UTC)

func newChecker() *Checker {
	return &Checker{Client: &http.Client{}, Now: func() time.Time { return fixedNow }}
}

func checkFor(url string, expected int, timeout time.Duration) config.Check {
	return config.Check{Name: "api", URL: url, Interval: time.Minute, Timeout: timeout, ExpectedStatus: expected}
}

func TestRunCapturesTLSCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ch := &Checker{Client: srv.Client(), Now: func() time.Time { return fixedNow }}
	res := ch.Run(context.Background(), checkFor(srv.URL, 200, time.Second))
	cert := srv.Certificate()
	issuer := cert.Issuer.CommonName
	if issuer == "" && len(cert.Issuer.Organization) > 0 {
		issuer = cert.Issuer.Organization[0]
	}
	if res.Status != StatusDown || res.Cert == nil {
		t.Fatalf("result = %+v, want down with certificate", res)
	}
	if res.Cert.Host != "127.0.0.1" || res.Cert.Issuer != issuer || res.Cert.Serial != cert.SerialNumber.Text(16) || !res.Cert.NotAfter.Equal(cert.NotAfter) {
		t.Fatalf("certificate = %+v, want host, issuer, serial and expiry from server", res.Cert)
	}
}

func TestRunCertificateFromFinalRedirectResponse(t *testing.T) {
	final := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(final.URL, "127.0.0.1", "example.com", 1), http.StatusFound)
	}))
	defer redirect.Close()
	client := redirect.Client()
	transport := client.Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		address = strings.Replace(address, "example.com:", "127.0.0.1:", 1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	client.Transport = transport
	defer transport.CloseIdleConnections()
	ch := &Checker{Client: client, Now: func() time.Time { return fixedNow }}
	res := ch.Run(context.Background(), checkFor(redirect.URL, 200, time.Second))
	if res.Status != StatusUp || res.Cert == nil || res.Cert.Host != "example.com" || res.Cert.Serial != final.Certificate().SerialNumber.Text(16) {
		t.Fatalf("result = %+v, want final server certificate", res)
	}
}

func TestRunPlainHTTPHasNoCertificate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	res := newChecker().Run(context.Background(), checkFor(srv.URL, 200, time.Second))
	if res.Status != StatusUp || res.Cert != nil {
		t.Fatalf("result = %+v, want up without certificate", res)
	}
}

func TestRunUpWhenStatusMatches(t *testing.T) {
	var gotMethod, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotUA = r.Method, r.UserAgent()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	res := newChecker().Run(context.Background(), checkFor(srv.URL, 204, time.Second))

	if res.Status != StatusUp {
		t.Fatalf("Status = %q, want %q (error %q)", res.Status, StatusUp, res.Error)
	}
	if res.CheckName != "api" || !res.At.Equal(fixedNow) || res.StatusCode != 204 || res.Error != "" {
		t.Fatalf("Result = %+v, want api at %v with status code 204 and no error", res, fixedNow)
	}
	if res.Latency <= 0 {
		t.Fatalf("Latency = %v, want > 0", res.Latency)
	}
	if gotMethod != http.MethodGet || gotUA != "up-monitor" {
		t.Fatalf("request = %s with User-Agent %q, want GET with up-monitor", gotMethod, gotUA)
	}
}

func TestRunDownOnUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	res := newChecker().Run(context.Background(), checkFor(srv.URL, 200, time.Second))

	if res.Status != StatusDown || res.StatusCode != 503 {
		t.Fatalf("Result = %+v, want down with status code 503", res)
	}
	if want := "unexpected status 503 (expected 200)"; res.Error != want {
		t.Fatalf("Error = %q, want %q", res.Error, want)
	}
	if res.Latency <= 0 {
		t.Fatalf("Latency = %v, want > 0", res.Latency)
	}
}

func TestRunFollowsRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/new", http.StatusMovedPermanently)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res := newChecker().Run(context.Background(), checkFor(srv.URL+"/old", 200, time.Second))

	if res.Status != StatusUp || res.StatusCode != 200 {
		t.Fatalf("Result = %+v, want up with final status code 200", res)
	}
}

func TestRunDownOnTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	res := newChecker().Run(context.Background(), checkFor(srv.URL, 200, 50*time.Millisecond))

	if res.Status != StatusDown || res.StatusCode != 0 {
		t.Fatalf("Result = %+v, want down without status code", res)
	}
	if want := "timeout after 50ms"; res.Error != want {
		t.Fatalf("Error = %q, want %q", res.Error, want)
	}
}

func TestRunDownOnConnectionError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	res := newChecker().Run(context.Background(), checkFor("http://"+addr, 200, time.Second))

	if res.Status != StatusDown || res.StatusCode != 0 {
		t.Fatalf("Result = %+v, want down without status code", res)
	}
	if !strings.Contains(res.Error, "connection refused") {
		t.Fatalf("Error = %q, want it to mention connection refused", res.Error)
	}
	if strings.Contains(res.Error, addr+"\": ") || strings.HasPrefix(res.Error, "Get ") {
		t.Fatalf("Error = %q, want the cause without the request prefix", res.Error)
	}
}

func TestRunReportsCanceledParentContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := newChecker().Run(ctx, checkFor(srv.URL, 200, time.Second))

	if res.Status != StatusCanceled {
		t.Fatalf("Status = %q, want %q so callers can discard it", res.Status, StatusCanceled)
	}
}
