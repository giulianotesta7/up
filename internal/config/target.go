package config

import (
	"net"
	"strconv"
)

// Target is what a check probes, for display and logs: the URL of an HTTP
// check or host:port of a TCP check (IPv6 hosts get brackets).
func (c Check) Target() string {
	if c.Kind == CheckTCP {
		return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	}
	return c.URL
}
