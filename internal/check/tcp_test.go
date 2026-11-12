package check

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

func tcpCheck(host string, port int, timeout time.Duration) config.Check {
	return config.Check{Name: "db", Kind: config.CheckTCP, Host: host, Port: port, Interval: time.Minute, Timeout: timeout}
}

func tcpChecker(dial func(ctx context.Context, network, address string) (net.Conn, error)) *Checker {
	return &Checker{Now: func() time.Time { return fixedNow }, Dial: dial}
}

type closeSpy struct {
	net.Conn
	closed chan struct{}
}

func (c closeSpy) Close() error {
	close(c.closed)
	return c.Conn.Close()
}

func TestTCPConnectIsUpAndClosesImmediately(t *testing.T) {
	spy := closeSpy{closed: make(chan struct{})}
	var network, address string
	ch := tcpChecker(func(ctx context.Context, n, a string) (net.Conn, error) {
		network, address = n, a
		time.Sleep(5 * time.Millisecond)
		client, server := net.Pipe()
		t.Cleanup(func() { server.Close() })
		spy.Conn = client
		return spy, nil
	})
	res := ch.Run(context.Background(), tcpCheck("db.example.com", 5432, time.Second))
	if res.Status != StatusUp || res.Error != "" || res.StatusCode != 0 || res.Cert != nil {
		t.Fatalf("result = %+v, want up, status code 0, no error, no certificate", res)
	}
	if res.CheckName != "db" || !res.At.Equal(fixedNow) {
		t.Fatalf("result = %+v, want check name and injected time", res)
	}
	if res.Latency < 5*time.Millisecond {
		t.Fatalf("latency = %s, want the connect time including the dial", res.Latency)
	}
	if network != "tcp" || address != "db.example.com:5432" {
		t.Fatalf("dialed %s %s, want tcp db.example.com:5432", network, address)
	}
	select {
	case <-spy.closed:
	default:
		t.Fatal("connection still open after the check returned")
	}
}

func TestTCPDialsIPv6WithBrackets(t *testing.T) {
	var address string
	ch := tcpChecker(func(ctx context.Context, n, a string) (net.Conn, error) {
		address = a
		client, server := net.Pipe()
		t.Cleanup(func() { server.Close() })
		return client, nil
	})
	if res := ch.Run(context.Background(), tcpCheck("::1", 5432, time.Second)); res.Status != StatusUp {
		t.Fatalf("result = %+v, want up", res)
	}
	if address != "[::1]:5432" {
		t.Fatalf("address = %q, want [::1]:5432", address)
	}
}

func TestTCPRefusedIsDownWithReason(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	res := New().Run(context.Background(), tcpCheck("127.0.0.1", port, time.Second))
	if res.Status != StatusDown || res.Error != "connection refused" || res.StatusCode != 0 || res.Latency != 0 {
		t.Fatalf("result = %+v, want down, %q, status code 0", res, "connection refused")
	}
}

func TestTCPDNSFailureIsDownWithReason(t *testing.T) {
	ch := tcpChecker(func(ctx context.Context, n, a string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{Err: "no such host", Name: "nope.invalid", IsNotFound: true}}
	})
	res := ch.Run(context.Background(), tcpCheck("nope.invalid", 80, time.Second))
	if res.Status != StatusDown || res.Error != "DNS lookup failed: no such host" {
		t.Fatalf("result = %+v, want down with a DNS reason", res)
	}
}

func TestTCPTimeoutIsDown(t *testing.T) {
	ch := tcpChecker(func(ctx context.Context, n, a string) (net.Conn, error) {
		<-ctx.Done()
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: ctx.Err()}
	})
	res := ch.Run(context.Background(), tcpCheck("db.example.com", 5432, 20*time.Millisecond))
	if res.Status != StatusDown || res.Error != "timeout after 20ms" {
		t.Fatalf("result = %+v, want down, timeout after 20ms", res)
	}
}

func TestTCPCancellationIsNotAResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := tcpChecker(func(ctx context.Context, n, a string) (net.Conn, error) {
		cancel()
		<-ctx.Done()
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: ctx.Err()}
	})
	res := ch.Run(ctx, tcpCheck("db.example.com", 5432, time.Second))
	if res.Status != StatusCanceled || res.Error != "" {
		t.Fatalf("result = %+v, want canceled without an error", res)
	}
}

func TestTCPOtherDialErrorKeepsCause(t *testing.T) {
	ch := tcpChecker(func(ctx context.Context, n, a string) (net.Conn, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errString("network is unreachable")}
	})
	res := ch.Run(context.Background(), tcpCheck("10.0.0.1", 1, time.Second))
	if res.Status != StatusDown || !strings.Contains(res.Error, "network is unreachable") || strings.Contains(res.Error, strconv.Itoa(1)+": ") {
		t.Fatalf("result = %+v, want down with the cause", res)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestHTTPKindStillUsesHTTPClient(t *testing.T) {
	dialed := false
	ch := newChecker()
	ch.Dial = func(ctx context.Context, n, a string) (net.Conn, error) {
		dialed = true
		return nil, errString("unused")
	}
	res := ch.Run(context.Background(), checkFor("http://127.0.0.1:1", 200, time.Second))
	if dialed || res.Status != StatusDown || res.Error == "" {
		t.Fatalf("dialed=%v result=%+v, want the HTTP path (refused) without the TCP dialer", dialed, res)
	}
}
