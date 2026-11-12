package config

import (
	"net"
	"strconv"
)

// Target is what a check probes, for display and logs: the URL of an HTTP
// check, host:port of a TCP check (IPv6 hosts get brackets), the host of a
// ping check or the record type and domain of a DNS check ("A example.com").
func (c Check) Target() string {
	switch c.Kind {
	case CheckTCP:
		return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	case CheckPing:
		return c.Host
	case CheckDNS:
		return c.Record + " " + c.Domain
	}
	return c.URL
}
