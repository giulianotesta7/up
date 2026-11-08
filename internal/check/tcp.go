package check

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
	"time"

	"github.com/heohlee/up/internal/config"
)

// DialFunc opens a connection; it is the seam TCP checks dial through.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// runTCP dials Host:Port under the timeout and closes at once. It proves a
// listener accepted the connection, nothing about TLS or the application.
func (ch *Checker) runTCP(ctx context.Context, c config.Check) Result {
	res := Result{CheckName: c.Name, At: ch.Now()}

	dialCtx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	dial := ch.Dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	start := time.Now()
	conn, err := dial(dialCtx, "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)))
	if err != nil {
		switch {
		case ctx.Err() != nil:
			res.Status = StatusCanceled
		case errors.Is(err, context.DeadlineExceeded) || isNetTimeout(err):
			res.Status, res.Error = StatusDown, fmt.Sprintf("timeout after %s", c.Timeout)
		default:
			res.Status, res.Error = StatusDown, dialCause(err)
		}
		return res
	}
	res.Latency = time.Since(start)
	_ = conn.Close()
	res.Status = StatusUp
	return res
}

func isNetTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// dialCause names why a dial failed without the "dial tcp host:port:" prefix,
// since the target is already shown next to the check.
func dialCause(err error) string {
	var dns *net.DNSError
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.As(err, &dns):
		return "DNS lookup failed: " + dns.Err
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}
