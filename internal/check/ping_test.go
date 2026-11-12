package check

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func pingCheck(host string, timeout time.Duration) config.Check {
	return config.Check{Name: "router", Kind: config.CheckPing, Host: host, Interval: time.Minute, Timeout: timeout}
}

// fakePinger records the addresses it is asked to ping and answers with
// reply, or waits for the deadline when reply is nil.
type fakePinger struct {
	mu       sync.Mutex
	pinged   []netip.Addr
	deadline time.Time
	reply    func(ctx context.Context) (time.Duration, error)
}

func (p *fakePinger) Ping(ctx context.Context, ip netip.Addr) (time.Duration, error) {
	p.mu.Lock()
	p.pinged = append(p.pinged, ip)
	p.deadline, _ = ctx.Deadline()
	p.mu.Unlock()
	if p.reply == nil {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return p.reply(ctx)
}

func pingChecker(p Pinger, lookup func(context.Context, string) ([]netip.Addr, error)) *Checker {
	return &Checker{Now: func() time.Time { return fixedNow }, Pinger: p, LookupIP: lookup}
}

func noLookup(t *testing.T) func(context.Context, string) ([]netip.Addr, error) {
	return func(context.Context, string) ([]netip.Addr, error) {
		t.Error("an IP address was resolved")
		return nil, errors.New("unused")
	}
}

func TestPingReplyIsUpWithTheRoundTripTime(t *testing.T) {
	p := &fakePinger{reply: func(context.Context) (time.Duration, error) { return 12 * time.Millisecond, nil }}
	start := time.Now()
	res := pingChecker(p, noLookup(t)).Run(context.Background(), pingCheck("192.168.1.1", 2*time.Second))
	if res.Status != StatusUp || res.Error != "" || res.Latency != 12*time.Millisecond || res.StatusCode != 0 || res.CheckName != "router" || !res.At.Equal(fixedNow) {
		t.Fatalf("result = %+v, want up with latency 12ms", res)
	}
	if len(p.pinged) != 1 || p.pinged[0] != netip.MustParseAddr("192.168.1.1") {
		t.Fatalf("pinged %v, want one echo to 192.168.1.1", p.pinged)
	}
	if d := p.deadline.Sub(start); d <= 0 || d > 2*time.Second+100*time.Millisecond {
		t.Fatalf("ping deadline %s after start, want the 2s timeout", d)
	}
}

func TestPingIPv6Literal(t *testing.T) {
	p := &fakePinger{reply: func(context.Context) (time.Duration, error) { return time.Millisecond, nil }}
	if res := pingChecker(p, noLookup(t)).Run(context.Background(), pingCheck("fd00::1", time.Second)); res.Status != StatusUp {
		t.Fatalf("result = %+v, want up", res)
	}
	if len(p.pinged) != 1 || p.pinged[0] != netip.MustParseAddr("fd00::1") {
		t.Fatalf("pinged %v", p.pinged)
	}
}

func TestPingWithoutReplyIsDown(t *testing.T) {
	p := &fakePinger{}
	res := pingChecker(p, noLookup(t)).Run(context.Background(), pingCheck("192.168.1.1", 20*time.Millisecond))
	if res.Status != StatusDown || res.Error != "no reply from 192.168.1.1 in 20ms" || res.Latency != 0 {
		t.Fatalf("result = %+v, want down, no reply from 192.168.1.1 in 20ms", res)
	}
}

func TestPingHostNameIsResolvedPreferringIPv4(t *testing.T) {
	p := &fakePinger{reply: func(context.Context) (time.Duration, error) { return time.Millisecond, nil }}
	var asked string
	lookup := func(_ context.Context, host string) ([]netip.Addr, error) {
		asked = host
		return []netip.Addr{netip.MustParseAddr("fd00::5"), netip.MustParseAddr("192.168.1.5")}, nil
	}
	res := pingChecker(p, lookup).Run(context.Background(), pingCheck("nas.home.arpa", time.Second))
	if res.Status != StatusUp || asked != "nas.home.arpa" || len(p.pinged) != 1 || p.pinged[0] != netip.MustParseAddr("192.168.1.5") {
		t.Fatalf("result = %+v, asked %q, pinged %v", res, asked, p.pinged)
	}
	lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("fd00::5")}, nil
	}
	p.pinged = nil
	if res := pingChecker(p, lookup).Run(context.Background(), pingCheck("nas.home.arpa", time.Second)); res.Status != StatusUp || p.pinged[0] != netip.MustParseAddr("fd00::5") {
		t.Fatalf("result = %+v, pinged %v, want the IPv6 address", res, p.pinged)
	}
}

func TestPingUnresolvableHostIsDown(t *testing.T) {
	p := &fakePinger{}
	for _, lookup := range []func(context.Context, string) ([]netip.Addr, error){
		func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no such host") },
		func(context.Context, string) ([]netip.Addr, error) { return nil, nil },
	} {
		res := pingChecker(p, lookup).Run(context.Background(), pingCheck("nas.home.arpa", time.Second))
		if res.Status != StatusDown || res.Error != "cannot resolve nas.home.arpa" {
			t.Fatalf("result = %+v, want down, cannot resolve nas.home.arpa", res)
		}
	}
	if len(p.pinged) != 0 {
		t.Fatalf("pinged %v without an address", p.pinged)
	}
}

func TestPingCancellationIsNotAResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &fakePinger{reply: func(ctx context.Context) (time.Duration, error) {
		cancel()
		<-ctx.Done()
		return 0, ctx.Err()
	}}
	if res := pingChecker(p, noLookup(t)).Run(ctx, pingCheck("192.168.1.1", time.Second)); res.Status != StatusCanceled || res.Error != "" {
		t.Fatalf("result = %+v, want canceled", res)
	}
}

func TestPingSendErrorIsDownWithItsCause(t *testing.T) {
	p := &fakePinger{reply: func(context.Context) (time.Duration, error) { return 0, errors.New("network is unreachable") }}
	if res := pingChecker(p, noLookup(t)).Run(context.Background(), pingCheck("192.168.1.1", time.Second)); res.Status != StatusDown || res.Error != "network is unreachable" {
		t.Fatalf("result = %+v, want down with the cause", res)
	}
}

func TestEchoMessagesRoundTrip(t *testing.T) {
	for _, ip := range []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")} {
		req, err := echoRequest(ip, 0x1234, 7, []byte("up-ping"))
		if err != nil {
			t.Fatal(err)
		}
		// A reply is the request with the reply type; matching ignores the
		// identifier, which the kernel sets for datagram sockets.
		var typ icmp.Type = ipv4.ICMPTypeEchoReply
		if ip.Is6() {
			typ = ipv6.ICMPTypeEchoReply
		}
		reply, err := (&icmp.Message{Type: typ, Body: &icmp.Echo{ID: 0x9999, Seq: 7, Data: []byte("up-ping")}}).Marshal(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(req) == 0 || !isEchoReply(ip, reply, 7, []byte("up-ping")) {
			t.Fatalf("%s: reply not recognized", ip)
		}
		if isEchoReply(ip, req, 7, []byte("up-ping")) || isEchoReply(ip, reply, 8, []byte("up-ping")) || isEchoReply(ip, reply, 7, []byte("other")) {
			t.Fatalf("%s: a request, another sequence or another payload matched", ip)
		}
	}
}

// TestSystemPingerLoopback sends one real echo request to 127.0.0.1. It is
// opt-in (UP_TEST_ICMP=1) and skips when this process may not open an ICMP
// socket; it never changes system settings.
func TestSystemPingerLoopback(t *testing.T) {
	if os.Getenv("UP_TEST_ICMP") != "1" {
		t.Skip("set UP_TEST_ICMP=1 to send a real ping to 127.0.0.1")
	}
	if DetectPings() != config.PingAllowed {
		t.Skip("this process cannot open an ICMP socket")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rtt, err := SystemPinger{}.Ping(ctx, netip.MustParseAddr("127.0.0.1"))
	if err != nil || rtt <= 0 {
		t.Fatalf("Ping(127.0.0.1) = %s, %v", rtt, err)
	}
}
