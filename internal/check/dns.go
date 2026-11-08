package check

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/heohlee/up/internal/config"
	"golang.org/x/net/dns/dnsmessage"
)

// resolvConf is where the system resolver's servers are listed.
const resolvConf = "/etc/resolv.conf"

// ednsSize is the UDP payload size advertised to resolvers; larger answers
// come back truncated and are asked again over TCP.
const ednsSize = 1232

var dnsTypes = map[string]dnsmessage.Type{
	"A": dnsmessage.TypeA, "AAAA": dnsmessage.TypeAAAA, "CNAME": dnsmessage.TypeCNAME,
	"MX": dnsmessage.TypeMX, "TXT": dnsmessage.TypeTXT,
}

// runDNS asks the resolver (the system's servers in order when empty) for
// Domain's Record over UDP, again over TCP when the answer is truncated. It
// is up when the reply has an answer of that type and, with Expected, one
// of them has that value (CNAME: the first). Latency is the query time of
// the server that answered.
func (ch *Checker) runDNS(ctx context.Context, c config.Check) Result {
	res := Result{CheckName: c.Name, At: ch.Now()}

	queryCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	servers := []string{config.ResolverAddress(c.Resolver)}
	if c.Resolver == "" {
		servers = ch.nameservers()
		if len(servers) == 0 {
			res.Status, res.Error = StatusDown, "no system resolver configured"
			return res
		}
	}
	typ := dnsTypes[c.Record]
	name, err := dnsmessage.NewName(strings.TrimSuffix(c.Domain, ".") + ".")
	if err != nil {
		res.Status, res.Error = StatusDown, fmt.Sprintf("domain %q cannot be queried", c.Domain)
		return res
	}
	var start time.Time
	var reply *dnsmessage.Message
	var server string
	var silent []string
	// The next server is only tried when one does not answer at all. Each
	// gets an even share of the time left, so a silent one leaves time for
	// the rest and the check still ends within its timeout.
	deadline, _ := queryCtx.Deadline()
	for i := range servers {
		server, start = servers[i], time.Now()
		serverCtx, cancelServer := context.WithTimeout(queryCtx, time.Until(deadline)/time.Duration(len(servers)-i))
		reply, err = ch.exchange(serverCtx, server, name, typ)
		cancelServer()
		if err != nil && (errors.Is(err, context.DeadlineExceeded) || isNetTimeout(err)) {
			silent = append(silent, server)
		}
		if err == nil || queryCtx.Err() != nil {
			break
		}
	}
	switch {
	case ctx.Err() != nil:
		res.Status = StatusCanceled
		return res
	case err != nil && (errors.Is(err, context.DeadlineExceeded) || isNetTimeout(err)):
		res.Status, res.Error = StatusDown, fmt.Sprintf("no reply from %s in %s", strings.Join(silent, ", "), config.ShortDuration(c.Timeout))
		return res
	case err != nil:
		res.Status, res.Error = StatusDown, fmt.Sprintf("cannot query %s: %s", server, dialCause(err))
		return res
	}
	res.Latency = time.Since(start)
	if reply.Header.RCode != dnsmessage.RCodeSuccess {
		res.Status, res.Error = StatusDown, fmt.Sprintf("%s for %s", rcodeName(reply.Header.RCode), c.Domain)
		return res
	}
	answers := answerValues(reply, typ)
	if len(answers) == 0 {
		res.Status, res.Error = StatusDown, fmt.Sprintf("no %s record for %s", c.Record, c.Domain)
		return res
	}
	res.Answer, res.Answers = answers[0], len(answers)
	if c.Expected == "" {
		res.Status = StatusUp
		return res
	}
	candidates := answers
	if c.Record == "CNAME" {
		candidates = answers[:1]
	}
	for _, a := range candidates {
		if answerMatches(c.Record, a, c.Expected) {
			res.Status, res.Answer = StatusUp, a
			return res
		}
	}
	got := answers[0]
	if len(answers) > 1 {
		got += fmt.Sprintf(" +%d", len(answers)-1)
	}
	res.Status, res.Error = StatusDown, fmt.Sprintf("%s = %s, want %s", c.Record, got, c.Expected)
	return res
}

// rcodeName is the name an RCODE has in dig and RFC 1035.
func rcodeName(r dnsmessage.RCode) string {
	switch r {
	case dnsmessage.RCodeNameError:
		return "NXDOMAIN"
	case dnsmessage.RCodeServerFailure:
		return "SERVFAIL"
	case dnsmessage.RCodeRefused:
		return "REFUSED"
	case dnsmessage.RCodeFormatError:
		return "FORMERR"
	case dnsmessage.RCodeNotImplemented:
		return "NOTIMP"
	}
	return fmt.Sprintf("RCODE %d", r)
}

// answerValues lists the reply's answers of type typ in order, as shown and
// compared: addresses, host names without the final dot, and each TXT
// record's strings joined.
func answerValues(m *dnsmessage.Message, typ dnsmessage.Type) []string {
	var out []string
	for _, rr := range m.Answers {
		if rr.Header.Type != typ {
			continue
		}
		switch b := rr.Body.(type) {
		case *dnsmessage.AResource:
			out = append(out, netip.AddrFrom4(b.A).String())
		case *dnsmessage.AAAAResource:
			out = append(out, netip.AddrFrom16(b.AAAA).String())
		case *dnsmessage.CNAMEResource:
			out = append(out, strings.TrimSuffix(b.CNAME.String(), "."))
		case *dnsmessage.MXResource:
			out = append(out, strings.TrimSuffix(b.MX.String(), "."))
		case *dnsmessage.TXTResource:
			out = append(out, strings.Join(b.TXT, ""))
		}
	}
	return out
}

// answerMatches compares one answer with the expected value: addresses by
// value, host names without regard to case or a final dot, text exactly.
func answerMatches(record, answer, expected string) bool {
	switch record {
	case "A", "AAAA":
		a, errA := netip.ParseAddr(answer)
		e, errE := netip.ParseAddr(expected)
		return errA == nil && errE == nil && a == e
	case "CNAME", "MX":
		return strings.EqualFold(strings.TrimSuffix(answer, "."), strings.TrimSuffix(expected, "."))
	}
	return answer == expected
}

func (ch *Checker) nameservers() []string {
	if ch.Nameservers != nil {
		return ch.Nameservers()
	}
	return resolvConfNameservers(resolvConf)
}

// resolvConfNameservers lists the valid nameserver addresses in a
// resolv.conf file as host:port with port 53; none when it cannot be read.
func resolvConfNameservers(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		if ip, err := netip.ParseAddr(fields[1]); err == nil {
			out = append(out, net.JoinHostPort(ip.String(), "53"))
		}
	}
	return out
}

// exchange sends one query to server over UDP and returns its reply, asking
// again over TCP when the UDP reply is truncated. A reply with another ID or
// question is ignored.
func (ch *Checker) exchange(ctx context.Context, server string, name dnsmessage.Name, typ dnsmessage.Type) (*dnsmessage.Message, error) {
	id := uint16(rand.IntN(1 << 16))
	query, err := buildQuery(id, name, typ)
	if err != nil {
		return nil, err
	}
	reply, err := ch.query(ctx, "udp", server, query, id, name, typ)
	if err != nil || !reply.Header.Truncated {
		return reply, err
	}
	return ch.query(ctx, "tcp", server, query, id, name, typ)
}

func buildQuery(id uint16, name dnsmessage.Name, typ dnsmessage.Type) ([]byte, error) {
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), dnsmessage.Header{ID: id, RecursionDesired: true})
	b.EnableCompression()
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: typ, Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	if err := b.StartAdditionals(); err != nil {
		return nil, err
	}
	var opt dnsmessage.ResourceHeader
	if err := opt.SetEDNS0(ednsSize, dnsmessage.RCodeSuccess, false); err != nil {
		return nil, err
	}
	if err := b.OPTResource(opt, dnsmessage.OPTResource{}); err != nil {
		return nil, err
	}
	return b.Finish()
}

func (ch *Checker) query(ctx context.Context, network, server string, query []byte, id uint16, name dnsmessage.Name, typ dnsmessage.Type) (*dnsmessage.Message, error) {
	dial := ch.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	conn, err := dial(ctx, network, server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	stream := network == "tcp"
	if stream {
		query = append(binary.BigEndian.AppendUint16(nil, uint16(len(query))), query...)
	}
	if _, err := conn.Write(query); err != nil {
		return nil, ctxErr(ctx, err)
	}
	buf := make([]byte, 65535)
	for {
		var n int
		if stream {
			var size [2]byte
			if _, err = io.ReadFull(conn, size[:]); err == nil {
				n = int(binary.BigEndian.Uint16(size[:]))
				_, err = io.ReadFull(conn, buf[:n])
			}
		} else {
			n, err = conn.Read(buf)
		}
		if err != nil {
			return nil, ctxErr(ctx, err)
		}
		var m dnsmessage.Message
		if m.Unpack(buf[:n]) != nil || !isReplyTo(&m, id, name, typ) {
			if stream {
				return nil, errors.New("malformed reply")
			}
			continue
		}
		return &m, nil
	}
}

// ctxErr prefers the context's error, so a deadline or a cancellation is
// reported as such rather than as the socket error it caused.
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func isReplyTo(m *dnsmessage.Message, id uint16, name dnsmessage.Name, typ dnsmessage.Type) bool {
	if !m.Header.Response || m.Header.ID != id || len(m.Questions) != 1 {
		return false
	}
	q := m.Questions[0]
	return q.Type == typ && q.Class == dnsmessage.ClassINET && strings.EqualFold(q.Name.String(), name.String())
}
