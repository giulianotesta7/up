package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Kinds of checks that probe the network below HTTP: one ICMP echo to a host,
// or one DNS query for a domain.
const (
	CheckPing = "ping"
	CheckDNS  = "dns"
)

// DNSRecords lists the record types a DNS check may query, in display order.
var DNSRecords = []string{"A", "AAAA", "CNAME", "MX", "TXT"}

// MaxDNSExpected is the longest expected DNS value in bytes.
const MaxDNSExpected = 1024

// DefaultDNSPort is the resolver port when the resolver gives none.
const DefaultDNSPort = 53

// PingPermission is whether the daemon can send ICMP echo requests, detected
// when `up serve` starts. The zero value is unknown: the daemon never
// reported it (it predates ping monitors or never ran).
type PingPermission string

// Ping permissions.
const (
	PingUnknown PingPermission = ""
	PingAllowed PingPermission = "allowed"
	PingDenied  PingPermission = "denied"
)

// PingDeniedReason is why a ping monitor stays pending on a daemon that
// cannot send pings.
const PingDeniedReason = "cannot send pings"

// Holds reports whether c waits instead of running on a daemon with this
// permission: only ping checks, only when pings are denied. A held check is
// pending (PingDeniedReason), records nothing and sends no alert.
func (p PingPermission) Holds(c Check) bool {
	return p == PingDenied && c.Kind == CheckPing
}

// hasDNSFields reports whether any DNS-only field is set.
func (c Check) hasDNSFields() bool {
	return c.Domain != "" || c.Record != "" || c.Resolver != "" || c.Expected != ""
}

var errDNSFields = errors.New("domain, record, resolver and expected value are only for dns checks")

// validateHost is the TCP and ping host rule: a name or IP without scheme,
// brackets or port; an IPv6 literal is written bare ("2001:db8::1").
func validateHost(host string) error {
	switch {
	case host == "":
		return errors.New("host is required")
	case strings.ContainsAny(host, " \t/@[]"):
		return fmt.Errorf("host %q must be a host name or IP address, without scheme, brackets or port", host)
	case strings.Contains(host, ":"):
		if _, err := netip.ParseAddr(host); err != nil {
			return fmt.Errorf("host %q must be a host name or IP address, without scheme, brackets or port", host)
		}
	}
	return nil
}

// validateNotHTTP rejects the HTTP-only fields on a check of another kind
// the daemon runs, with the messages TCP checks use, and the TLS switch.
func validateNotHTTP(c Check) []error {
	var errs []error
	if c.URL != "" {
		errs = append(errs, errors.New("url is only for http checks"))
	}
	if c.ExpectedStatus != 0 {
		errs = append(errs, errors.New("expected status is only for http checks"))
	}
	if c.Method != "" || len(c.Headers) > 0 || !c.Body.Empty() || c.BodyEdit != EditKeep || c.ExpectText != "" {
		errs = append(errs, errors.New("method, headers, body and expected text are only for http checks"))
	}
	if len(c.Conditions) > 0 {
		errs = append(errs, errors.New("conditions are only for http checks"))
	}
	if c.TLSOff {
		errs = append(errs, errors.New("tls is only for http checks"))
	}
	return errs
}

// validatePingFields checks the host (see validateHost) and that no field
// of another kind is set.
func validatePingFields(c Check) []error {
	var errs []error
	if err := validateHost(c.Host); err != nil {
		errs = append(errs, err)
	}
	if c.Port != 0 {
		errs = append(errs, errors.New("port is only for tcp checks"))
	}
	if c.hasDNSFields() {
		errs = append(errs, errDNSFields)
	}
	return append(errs, validateNotHTTP(c)...)
}

// validateDNSFields checks the domain, record type, resolver and expected
// value and that no field of another kind is set. The expected value must be
// an address of the record's family for A and AAAA and a host name for CNAME
// and MX; a TXT value is any text.
func validateDNSFields(c Check) []error {
	var errs []error
	switch {
	case c.Domain == "":
		errs = append(errs, errors.New("domain is required"))
	case !validDomain(c.Domain):
		errs = append(errs, fmt.Errorf("domain %q must be a domain name like example.com", c.Domain))
	}
	switch {
	case c.Record == "":
		errs = append(errs, errors.New("record is required"))
	case !slices.Contains(DNSRecords, c.Record):
		errs = append(errs, fmt.Errorf("record %q must be one of %s", c.Record, strings.Join(DNSRecords, ", ")))
	}
	if c.Resolver != "" {
		if err := validateResolver(c.Resolver); err != nil {
			errs = append(errs, err)
		}
	}
	if len(c.Expected) > MaxDNSExpected {
		errs = append(errs, fmt.Errorf("expected value must be at most %d bytes", MaxDNSExpected))
	} else if c.Expected != "" {
		if err := validateExpected(c.Record, c.Expected); err != nil {
			errs = append(errs, err)
		}
	}
	if c.Host != "" || c.Port != 0 {
		errs = append(errs, errors.New("host and port are not used by dns checks"))
	}
	return append(errs, validateNotHTTP(c)...)
}

func validateExpected(record, v string) error {
	switch record {
	case "A", "AAAA":
		ip, err := netip.ParseAddr(v)
		family := "IPv4"
		if record == "AAAA" {
			family = "IPv6"
		}
		if err != nil || ip.Zone() != "" || (record == "A") != ip.Is4() {
			return fmt.Errorf("expected value %q must be an %s address for %s records", v, family, record)
		}
	case "CNAME", "MX":
		if !validDomain(v) {
			return fmt.Errorf("expected value %q must be a host name for %s records", v, record)
		}
	}
	return nil
}

// validDomain reports whether s is a DNS name: dot-separated labels of 1 to
// 63 letters, digits, hyphens or underscores, at most 253 bytes, with an
// optional final dot.
func validDomain(s string) bool {
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// splitResolver splits a resolver into host and port: "host", "host:port",
// "[ipv6]" or "[ipv6]:port". ok is false when it is none of those.
func splitResolver(s string) (host string, port int, ok bool) {
	port = DefaultDNSPort
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		host = s[1 : len(s)-1]
	} else if strings.Count(s, ":") == 1 || strings.HasPrefix(s, "[") {
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			return "", 0, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return h, 0, false
		}
		host, port = h, n
	} else {
		host = s
	}
	return host, port, true
}

func validateResolver(s string) error {
	usage := fmt.Errorf("resolver %q must be host or host:port with a port between 1 and 65535, like 192.0.2.53 or [fd00::53]:53", s)
	if ip, err := netip.ParseAddr(s); err == nil && ip.Is6() {
		return fmt.Errorf("resolver %q must put an IPv6 address in brackets, like [fd00::53]:53", s)
	}
	host, port, ok := splitResolver(s)
	if !ok || port == 0 || host == "" {
		return usage
	}
	bracketed := strings.HasPrefix(s, "[")
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Is6() != bracketed {
			return usage
		}
		return nil
	}
	if bracketed || !validDomain(host) {
		return usage
	}
	return nil
}

// ResolverAddress is the host:port a DNS check queries for resolver, with
// the default port 53, or "" for the system resolver. The resolver must be
// valid.
func ResolverAddress(resolver string) string {
	if resolver == "" {
		return ""
	}
	host, port, _ := splitResolver(resolver)
	return net.JoinHostPort(host, strconv.Itoa(port))
}
