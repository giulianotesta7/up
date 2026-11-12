package check

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"golang.org/x/net/dns/dnsmessage"
)

// dnsStub answers DNS queries over UDP and TCP on one loopback port. answer
// builds the response to a query; udp, when set, replaces it for UDP only.
type dnsStub struct {
	addr     string
	answer   func(q dnsmessage.Message) dnsmessage.Message
	udp      func(q dnsmessage.Message) (dnsmessage.Message, bool)
	tcpCount atomic.Int32
	udpCount atomic.Int32
	mu       sync.Mutex
	asked    dnsmessage.Question
}

// question is the last question the stub received.
func (s *dnsStub) question() dnsmessage.Question {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked
}

func (s *dnsStub) record(q dnsmessage.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(q.Questions) > 0 {
		s.asked = q.Questions[0]
	}
}

func startDNSStub(t *testing.T, host string, answer func(q dnsmessage.Message) dnsmessage.Message) *dnsStub {
	return startDNSStubUDP(t, host, answer, nil)
}

func startDNSStubUDP(t *testing.T, host string, answer func(q dnsmessage.Message) dnsmessage.Message, udp func(q dnsmessage.Message) (dnsmessage.Message, bool)) *dnsStub {
	t.Helper()
	s := &dnsStub{answer: answer, udp: udp}
	var pc net.PacketConn
	var ln net.Listener
	for range 20 {
		var err error
		pc, err = net.ListenPacket("udp", net.JoinHostPort(host, "0"))
		if err != nil {
			t.Skipf("no %s loopback: %v", host, err)
		}
		if ln, err = net.Listen("tcp", pc.LocalAddr().String()); err == nil {
			break
		}
		pc.Close()
		pc = nil
	}
	if pc == nil {
		t.Fatal("no free port for both UDP and TCP")
	}
	s.addr = pc.LocalAddr().String()
	var wg sync.WaitGroup
	t.Cleanup(func() { pc.Close(); ln.Close(); wg.Wait() })
	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]byte, 65535)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			s.udpCount.Add(1)
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil {
				continue
			}
			s.record(q)
			resp, send := s.answer(q), true
			if s.udp != nil {
				resp, send = s.udp(q)
			}
			if !send {
				continue
			}
			b, _ := resp.Pack()
			pc.WriteTo(b, from)
		}
	}()
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.tcpCount.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				var size [2]byte
				if _, err := io.ReadFull(conn, size[:]); err != nil {
					return
				}
				buf := make([]byte, binary.BigEndian.Uint16(size[:]))
				if _, err := io.ReadFull(conn, buf); err != nil {
					return
				}
				var q dnsmessage.Message
				if q.Unpack(buf) != nil {
					return
				}
				s.record(q)
				resp := s.answer(q)
				b, _ := resp.Pack()
				conn.Write(binary.BigEndian.AppendUint16(nil, uint16(len(b))))
				conn.Write(b)
			}()
		}
	}()
	return s
}

func reply(q dnsmessage.Message, rcode dnsmessage.RCode, answers ...dnsmessage.Resource) dnsmessage.Message {
	return dnsmessage.Message{
		Header:    dnsmessage.Header{ID: q.Header.ID, Response: true, RecursionDesired: q.Header.RecursionDesired, RCode: rcode},
		Questions: q.Questions,
		Answers:   answers,
	}
}

func rr(q dnsmessage.Message, body dnsmessage.ResourceBody) dnsmessage.Resource {
	return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Class: dnsmessage.ClassINET, TTL: 60}, Body: body}
}

func a(ip string) dnsmessage.ResourceBody {
	return &dnsmessage.AResource{A: netip.MustParseAddr(ip).As4()}
}

func name(s string) dnsmessage.Name { return dnsmessage.MustNewName(s) }

func dnsCheck(domain, record, resolver, expected string) config.Check {
	return config.Check{Name: "nas", Kind: config.CheckDNS, Domain: domain, Record: record, Resolver: resolver, Expected: expected, Interval: time.Minute, Timeout: time.Second}
}

func dnsChecker() *Checker { return &Checker{Now: func() time.Time { return fixedNow }} }

func TestDNSAnyAnswerMatchesExpected(t *testing.T) {
	stub := startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess,
			rr(q, &dnsmessage.CNAMEResource{CNAME: name("store.home.arpa.")}),
			rr(q, a("203.0.113.9")), rr(q, a("203.0.113.10")))
	})
	res := dnsChecker().Run(context.Background(), dnsCheck("nas.home.arpa", "A", stub.addr, "203.0.113.10"))
	if res.Status != StatusUp || res.Error != "" || res.CheckName != "nas" || !res.At.Equal(fixedNow) || res.StatusCode != 0 {
		t.Fatalf("result = %+v, want up", res)
	}
	// The answer shown is the one that matched, out of every A record.
	if res.Answer != "203.0.113.10" || res.Answers != 2 {
		t.Fatalf("answer = %q of %d, want the matching one of two A records", res.Answer, res.Answers)
	}
	if asked := stub.question(); asked.Name.String() != "nas.home.arpa." || asked.Type != dnsmessage.TypeA || asked.Class != dnsmessage.ClassINET {
		t.Fatalf("question = %+v", asked)
	}
	// Without an expected value any answer is up and the first is shown.
	if res := dnsChecker().Run(context.Background(), dnsCheck("nas.home.arpa.", "A", stub.addr, "")); res.Status != StatusUp || res.Answer != "203.0.113.9" || res.Answers != 2 {
		t.Fatalf("no expected value: %+v", res)
	}
}

func TestDNSMismatchIsDownWithTheShortReason(t *testing.T) {
	stub := startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess, rr(q, a("203.0.113.9")))
	})
	res := dnsChecker().Run(context.Background(), dnsCheck("nas.home.arpa", "A", stub.addr, "203.0.113.10"))
	if res.Status != StatusDown || res.Error != "A = 203.0.113.9, want 203.0.113.10" || res.Answer != "203.0.113.9" || res.Answers != 1 {
		t.Fatalf("result = %+v, want down, A = 203.0.113.9, want 203.0.113.10", res)
	}
	stub = startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess, rr(q, a("203.0.113.9")), rr(q, a("203.0.113.8")))
	})
	if res := dnsChecker().Run(context.Background(), dnsCheck("nas.home.arpa", "A", stub.addr, "203.0.113.10")); res.Error != "A = 203.0.113.9 +1, want 203.0.113.10" {
		t.Fatalf("reason with two answers = %q", res.Error)
	}
}

func TestDNSRecordTypesCompareTheirValue(t *testing.T) {
	tests := []struct {
		record, expected string
		body             func(q dnsmessage.Message) []dnsmessage.Resource
		answers          []string
	}{
		{"AAAA", "2001:db8::10", func(q dnsmessage.Message) []dnsmessage.Resource {
			return []dnsmessage.Resource{rr(q, &dnsmessage.AAAAResource{AAAA: netip.MustParseAddr("2001:db8:0::10").As16()})}
		}, []string{"2001:db8::10"}},
		// MX compares the host only; priority is ignored, case and the final dot too.
		{"MX", "Mail.Example.com.", func(q dnsmessage.Message) []dnsmessage.Resource {
			return []dnsmessage.Resource{rr(q, &dnsmessage.MXResource{Pref: 20, MX: name("backup.example.com.")}), rr(q, &dnsmessage.MXResource{Pref: 10, MX: name("mail.example.com.")})}
		}, []string{"mail.example.com", "backup.example.com"}},
		// TXT joins the strings of one record into one value.
		{"TXT", "v=spf1 include:_spf.example.com ~all", func(q dnsmessage.Message) []dnsmessage.Resource {
			return []dnsmessage.Resource{rr(q, &dnsmessage.TXTResource{TXT: []string{"v=spf1 include:", "_spf.example.com ~all"}})}
		}, []string{"v=spf1 include:_spf.example.com ~all"}},
		// CNAME compares the first target.
		{"CNAME", "edge.example.net", func(q dnsmessage.Message) []dnsmessage.Resource {
			return []dnsmessage.Resource{rr(q, &dnsmessage.CNAMEResource{CNAME: name("edge.example.net.")})}
		}, []string{"edge.example.net"}},
	}
	for _, tt := range tests {
		t.Run(tt.record, func(t *testing.T) {
			stub := startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
				return reply(q, dnsmessage.RCodeSuccess, tt.body(q)...)
			})
			res := dnsChecker().Run(context.Background(), dnsCheck("example.com", tt.record, stub.addr, tt.expected))
			if res.Status != StatusUp || res.Answer != tt.answers[0] || res.Answers != len(tt.answers) {
				t.Fatalf("result = %+v, want up with %q", res, tt.answers)
			}
			if asked := stub.question().Type; asked.String() != "Type"+tt.record {
				t.Fatalf("asked %s, want %s", asked, tt.record)
			}
		})
	}
	stub := startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess, rr(q, &dnsmessage.TXTResource{TXT: []string{"v=spf1", " ~all"}}))
	})
	if res := dnsChecker().Run(context.Background(), dnsCheck("example.com", "TXT", stub.addr, "v=spf1")); res.Status != StatusDown || res.Error != "TXT = v=spf1 ~all, want v=spf1" {
		t.Fatalf("TXT compares the whole string: %+v", res)
	}
}

func TestDNSFailuresAreDown(t *testing.T) {
	tests := []struct {
		rcode dnsmessage.RCode
		want  string
	}{
		{dnsmessage.RCodeNameError, "NXDOMAIN for nas.home.arpa"},
		{dnsmessage.RCodeServerFailure, "SERVFAIL for nas.home.arpa"},
		{dnsmessage.RCodeRefused, "REFUSED for nas.home.arpa"},
		{dnsmessage.RCodeSuccess, "no A record for nas.home.arpa"},
	}
	for _, tt := range tests {
		stub := startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
			// An answer of another type is no answer.
			return reply(q, tt.rcode, rr(q, &dnsmessage.CNAMEResource{CNAME: name("x.home.arpa.")}))
		})
		res := dnsChecker().Run(context.Background(), dnsCheck("nas.home.arpa", "A", stub.addr, ""))
		if res.Status != StatusDown || res.Error != tt.want {
			t.Errorf("rcode %s: result = %+v, want down, %q", tt.rcode, res, tt.want)
		}
	}
}

func TestDNSTruncatedAnswerIsRetriedOverTCP(t *testing.T) {
	stub := startDNSStubUDP(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess, rr(q, &dnsmessage.TXTResource{TXT: []string{"long"}}))
	}, func(q dnsmessage.Message) (dnsmessage.Message, bool) {
		m := reply(q, dnsmessage.RCodeSuccess)
		m.Header.Truncated = true
		return m, true
	})
	res := dnsChecker().Run(context.Background(), dnsCheck("example.com", "TXT", stub.addr, "long"))
	if res.Status != StatusUp || stub.udpCount.Load() != 1 || stub.tcpCount.Load() != 1 {
		t.Fatalf("result = %+v, udp %d tcp %d, want up after one UDP and one TCP query", res, stub.udpCount.Load(), stub.tcpCount.Load())
	}
}

func TestDNSNoReplyIsDownWithTheResolver(t *testing.T) {
	stub := startDNSStubUDP(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message { return reply(q, dnsmessage.RCodeSuccess) },
		func(dnsmessage.Message) (dnsmessage.Message, bool) { return dnsmessage.Message{}, false })
	c := dnsCheck("nas.home.arpa", "A", stub.addr, "")
	c.Timeout = 50 * time.Millisecond
	res := dnsChecker().Run(context.Background(), c)
	if res.Status != StatusDown || res.Error != "no reply from "+stub.addr+" in 50ms" {
		t.Fatalf("result = %+v, want down, no reply from %s in 50ms", res, stub.addr)
	}
}

func TestDNSIgnoresAReplyToAnotherQuery(t *testing.T) {
	stub := startDNSStubUDP(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess, rr(q, a("203.0.113.10")))
	}, func(q dnsmessage.Message) (dnsmessage.Message, bool) {
		m := reply(q, dnsmessage.RCodeSuccess, rr(q, a("198.51.100.1")))
		m.Header.ID++
		return m, true
	})
	c := dnsCheck("nas.home.arpa", "A", stub.addr, "")
	c.Timeout = 50 * time.Millisecond
	if res := dnsChecker().Run(context.Background(), c); res.Status != StatusDown || res.Error != "no reply from "+stub.addr+" in 50ms" {
		t.Fatalf("result = %+v, want the forged reply ignored", res)
	}
}

func TestDNSIPv6ResolverInBrackets(t *testing.T) {
	stub := startDNSStub(t, "::1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess, rr(q, a("203.0.113.10")))
	})
	if res := dnsChecker().Run(context.Background(), dnsCheck("nas.home.arpa", "A", stub.addr, "203.0.113.10")); res.Status != StatusUp {
		t.Fatalf("result = %+v via %s, want up", res, stub.addr)
	}
}

func TestDNSEmptyResolverUsesTheSystemNameservers(t *testing.T) {
	stub := startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess, rr(q, a("203.0.113.10")))
	})
	ch := dnsChecker()
	ch.Nameservers = func() []string { return []string{stub.addr} }
	if res := ch.Run(context.Background(), dnsCheck("nas.home.arpa", "A", "", "203.0.113.10")); res.Status != StatusUp {
		t.Fatalf("result = %+v, want up through the system nameserver", res)
	}
	ch.Nameservers = func() []string { return nil }
	if res := ch.Run(context.Background(), dnsCheck("nas.home.arpa", "A", "", "")); res.Status != StatusDown || res.Error != "no system resolver configured" {
		t.Fatalf("result = %+v, want down without a nameserver", res)
	}
}

func TestDNSSystemResolverTriesTheNextServerWhenOneRefuses(t *testing.T) {
	stub := startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess, rr(q, a("203.0.113.10")))
	})
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := pc.LocalAddr().String()
	pc.Close()
	ch := dnsChecker()
	ch.Nameservers = func() []string { return []string{closed, stub.addr} }
	if res := ch.Run(context.Background(), dnsCheck("nas.home.arpa", "A", "", "")); res.Status != StatusUp || res.Answer != "203.0.113.10" {
		t.Fatalf("result = %+v, want up from the second nameserver", res)
	}
}

// silentStub reads queries and never answers them.
func silentStub(t *testing.T) *dnsStub {
	return startDNSStubUDP(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message { return reply(q, dnsmessage.RCodeSuccess) },
		func(dnsmessage.Message) (dnsmessage.Message, bool) { return dnsmessage.Message{}, false })
}

func TestDNSSystemResolverTriesTheNextServerWhenOneIsSilent(t *testing.T) {
	silent := silentStub(t)
	stub := startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess, rr(q, a("203.0.113.10")))
	})
	ch := dnsChecker()
	ch.Nameservers = func() []string { return []string{silent.addr, stub.addr} }
	c := dnsCheck("nas.home.arpa", "A", "", "")
	c.Timeout = 400 * time.Millisecond
	start := time.Now()
	res := ch.Run(context.Background(), c)
	if res.Status != StatusUp || res.Answer != "203.0.113.10" || res.Error != "" {
		t.Fatalf("result = %+v, want up from the second nameserver", res)
	}
	if elapsed := time.Since(start); elapsed > c.Timeout {
		t.Fatalf("took %s, want within the %s timeout", elapsed, c.Timeout)
	}
	if silent.udpCount.Load() != 1 || stub.udpCount.Load() != 1 {
		t.Fatalf("queries: silent %d, second %d, want one each", silent.udpCount.Load(), stub.udpCount.Load())
	}
}

func TestDNSSystemResolverAnswerFromTheFirstServerIsFinal(t *testing.T) {
	first := startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeServerFailure)
	})
	second := startDNSStub(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message {
		return reply(q, dnsmessage.RCodeSuccess, rr(q, a("203.0.113.10")))
	})
	ch := dnsChecker()
	ch.Nameservers = func() []string { return []string{first.addr, second.addr} }
	res := ch.Run(context.Background(), dnsCheck("nas.home.arpa", "A", "", ""))
	if res.Status != StatusDown || res.Error != "SERVFAIL for nas.home.arpa" {
		t.Fatalf("result = %+v, want down, SERVFAIL for nas.home.arpa", res)
	}
	if n := second.udpCount.Load() + second.tcpCount.Load(); n != 0 {
		t.Fatalf("second nameserver asked %d times, want never", n)
	}
}

func TestDNSSystemResolverAllSilentIsDownWithinTheTimeout(t *testing.T) {
	first, second := silentStub(t), silentStub(t)
	ch := dnsChecker()
	ch.Nameservers = func() []string { return []string{first.addr, second.addr} }
	c := dnsCheck("nas.home.arpa", "A", "", "")
	c.Timeout = 200 * time.Millisecond
	start := time.Now()
	res := ch.Run(context.Background(), c)
	elapsed := time.Since(start)
	want := "no reply from " + first.addr + ", " + second.addr + " in 200ms"
	if res.Status != StatusDown || res.Error != want {
		t.Fatalf("result = %+v, want down, %s", res, want)
	}
	if elapsed > c.Timeout+100*time.Millisecond {
		t.Fatalf("took %s, want about the %s timeout", elapsed, c.Timeout)
	}
	if first.udpCount.Load() != 1 || second.udpCount.Load() != 1 {
		t.Fatalf("queries: first %d, second %d, want one each", first.udpCount.Load(), second.udpCount.Load())
	}
}

func TestDNSCancellationIsNotAResult(t *testing.T) {
	stub := startDNSStubUDP(t, "127.0.0.1", func(q dnsmessage.Message) dnsmessage.Message { return reply(q, dnsmessage.RCodeSuccess) },
		func(dnsmessage.Message) (dnsmessage.Message, bool) { return dnsmessage.Message{}, false })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if res := dnsChecker().Run(ctx, dnsCheck("nas.home.arpa", "A", stub.addr, "")); res.Status != StatusCanceled || res.Error != "" {
		t.Fatalf("result = %+v, want canceled", res)
	}
}

func TestResolvConfNameservers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	content := "# comment\nsearch home.arpa\nnameserver 127.0.0.53\nnameserver fd00::53\nnameserver fe80::1%eth0\noptions edns0\nnameserver bogus\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{"127.0.0.53:53", "[fd00::53]:53", "[fe80::1%eth0]:53"}
	if got := resolvConfNameservers(path); !reflect.DeepEqual(got, want) {
		t.Fatalf("nameservers = %q, want %q", got, want)
	}
	if got := resolvConfNameservers(filepath.Join(t.TempDir(), "missing")); got != nil {
		t.Fatalf("missing file = %q, want none", got)
	}
}
