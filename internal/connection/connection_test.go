package connection

import (
	"errors"
	"testing"
)

func TestNormalizeURL(t *testing.T) {
	good := map[string]string{
		"https://up.acme.com":            "https://up.acme.com",
		"  https://up.acme.com/  ":       "https://up.acme.com",
		"https://up.acme.com:8443/base/": "https://up.acme.com:8443/base",
		"HTTPS://UP.ACME.COM":            "https://up.acme.com",
	}
	for in, want := range good {
		got, err := NormalizeURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestNormalizeURLRejects(t *testing.T) {
	if _, err := NormalizeURL("http://up.acme.com"); !errors.Is(err, ErrPlainHTTP) {
		t.Errorf("http: got %v, want ErrPlainHTTP", err)
	}
	for _, in := range []string{"", "   ", "up.acme.com", "ftp://up.acme.com", "https://", "https://user:pw@up.acme.com", "https://up.acme.com?x=1", "https://up.acme.com#frag"} {
		if _, err := NormalizeURL(in); err == nil || errors.Is(err, ErrPlainHTTP) {
			t.Errorf("NormalizeURL(%q) err = %v, want a validation error", in, err)
		}
	}
}
