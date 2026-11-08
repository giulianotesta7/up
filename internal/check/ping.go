package check

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"time"

	"github.com/heohlee/up/internal/config"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// Pinger sends one ICMP echo request to ip and returns the round-trip time
// of its reply. It waits until ctx ends; a missing reply is ctx's error or a
// timeout.
type Pinger interface {
	Ping(ctx context.Context, ip netip.Addr) (time.Duration, error)
}

// errNoPingSocket means neither kind of ICMP socket could be opened.
var errNoPingSocket = errors.New(config.PingDeniedReason)

// runPing sends one echo request to Host under the timeout, after resolving
// a host name (IPv4 first). It proves the host answers ICMP, nothing about
// its services. Latency is the round-trip time.
func (ch *Checker) runPing(ctx context.Context, c config.Check) Result {
	res := Result{CheckName: c.Name, At: ch.Now()}

	pingCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	ip, err := ch.pingAddress(pingCtx, c.Host)
	if err != nil {
		if ctx.Err() != nil {
			res.Status = StatusCanceled
		} else {
			res.Status, res.Error = StatusDown, "cannot resolve "+c.Host
		}
		return res
	}
	pinger := ch.Pinger
	if pinger == nil {
		pinger = SystemPinger{}
	}
	rtt, err := pinger.Ping(pingCtx, ip)
	switch {
	case ctx.Err() != nil:
		res.Status = StatusCanceled
	case err == nil:
		res.Status, res.Latency = StatusUp, rtt
	case errors.Is(err, context.DeadlineExceeded) || isNetTimeout(err):
		res.Status, res.Error = StatusDown, fmt.Sprintf("no reply from %s in %s", c.Host, config.ShortDuration(c.Timeout))
	default:
		res.Status, res.Error = StatusDown, dialCause(err)
	}
	return res
}

// pingAddress is host itself when it is an IP address, otherwise its first
// IPv4 address, or its first address when it has no IPv4 one.
func (ch *Checker) pingAddress(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), nil
	}
	lookup := ch.LookupIP
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(ips) == 0 {
		return netip.Addr{}, errors.New("no address")
	}
	for _, ip := range ips {
		if ip.Unmap().Is4() {
			return ip.Unmap(), nil
		}
	}
	return ips[0], nil
}

// SystemPinger sends echo requests through an unprivileged ICMP datagram
// socket (a Linux "ping socket", allowed to the groups in
// net.ipv4.ping_group_range) or, when that is refused, a raw ICMP socket
// (CAP_NET_RAW). Each ping opens its own socket.
type SystemPinger struct{}

// Ping implements Pinger.
func (SystemPinger) Ping(ctx context.Context, ip netip.Addr) (time.Duration, error) {
	conn, raw, err := listenICMP(ip.Is4())
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	seq := rand.IntN(1 << 16)
	payload := make([]byte, 16)
	_, _ = crand.Read(payload)
	// A datagram socket's identifier is set by the kernel, which delivers
	// only this socket's replies; a raw socket sees every reply and is
	// matched by sequence and payload.
	msg, err := echoRequest(ip, rand.IntN(1<<16), seq, payload)
	if err != nil {
		return 0, err
	}
	var dst net.Addr = &net.UDPAddr{IP: ip.AsSlice(), Zone: ip.Zone()}
	if raw {
		dst = &net.IPAddr{IP: ip.AsSlice(), Zone: ip.Zone()}
	}
	start := time.Now()
	if _, err := conn.WriteTo(msg, dst); err != nil {
		return 0, err
	}
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, err
		}
		if sameIP(from, ip) && isEchoReply(ip, buf[:n], seq, payload) {
			return time.Since(start), nil
		}
	}
}

// listenICMP opens a datagram ICMP socket of ip's family, or a raw one when
// that is refused; raw tells which.
func listenICMP(v4 bool) (conn *icmp.PacketConn, raw bool, err error) {
	dgram, rawNet, addr := "udp6", "ip6:ipv6-icmp", "::"
	if v4 {
		dgram, rawNet, addr = "udp4", "ip4:icmp", "0.0.0.0"
	}
	if conn, err := icmp.ListenPacket(dgram, addr); err == nil {
		return conn, false, nil
	}
	if conn, err := icmp.ListenPacket(rawNet, addr); err == nil {
		return conn, true, nil
	}
	return nil, false, errNoPingSocket
}

// DetectPings reports whether this process can open an ICMP socket of
// either kind for either family, which is what ping checks need. It sends
// nothing.
func DetectPings() config.PingPermission {
	for _, v4 := range []bool{true, false} {
		if conn, _, err := listenICMP(v4); err == nil {
			conn.Close()
			return config.PingAllowed
		}
	}
	return config.PingDenied
}

func sameIP(from net.Addr, ip netip.Addr) bool {
	var got net.IP
	switch a := from.(type) {
	case *net.UDPAddr:
		got = a.IP
	case *net.IPAddr:
		got = a.IP
	default:
		return false
	}
	addr, ok := netip.AddrFromSlice(got)
	return ok && addr.Unmap() == ip.WithZone("").Unmap()
}

// echoRequest is an ICMP (IPv4) or ICMPv6 echo request. The kernel fills in
// the ICMPv6 checksum.
func echoRequest(ip netip.Addr, id, seq int, payload []byte) ([]byte, error) {
	var typ icmp.Type = ipv4.ICMPTypeEcho
	if ip.Is6() {
		typ = ipv6.ICMPTypeEchoRequest
	}
	return (&icmp.Message{Type: typ, Body: &icmp.Echo{ID: id, Seq: seq, Data: payload}}).Marshal(nil)
}

// isEchoReply reports whether b is the echo reply to the request with seq
// and payload; the identifier is not compared.
func isEchoReply(ip netip.Addr, b []byte, seq int, payload []byte) bool {
	proto, want := 1, icmp.Type(ipv4.ICMPTypeEchoReply)
	if ip.Is6() {
		proto, want = 58, ipv6.ICMPTypeEchoReply
	}
	m, err := icmp.ParseMessage(proto, b)
	if err != nil || m.Type != want {
		return false
	}
	echo, ok := m.Body.(*icmp.Echo)
	return ok && echo.Seq == seq && bytes.Equal(echo.Data, payload)
}
