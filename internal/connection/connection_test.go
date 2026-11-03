package connection

import "testing"

func TestNormalizeURL(t *testing.T) {
	good := map[string]string{
		"https://up.acme.com":            "https://up.acme.com",
		"  https://up.acme.com/  ":       "https://up.acme.com",
		"https://up.acme.com:8443/base/": "https://up.acme.com:8443/base",
		"HTTPS://UP.ACME.COM":            "https://up.acme.com",
		"http://up.acme.com":             "http://up.acme.com",
		"HTTP://10.0.0.5:8443/":          "http://10.0.0.5:8443",
	}
	for in, want := range good {
		got, err := NormalizeURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestNormalizeURLRejects(t *testing.T) {
	for _, in := range []string{"", "   ", "up.acme.com", "ftp://up.acme.com", "https://", "http://", "https://user:pw@up.acme.com", "http://user:pw@up.acme.com", "https://up.acme.com?x=1", "https://up.acme.com#frag"} {
		if _, err := NormalizeURL(in); err == nil {
			t.Errorf("NormalizeURL(%q) accepted, want a validation error", in)
		}
	}
}

func TestIsPlainHTTP(t *testing.T) {
	for in, want := range map[string]bool{"http://a.b": true, "  HTTP://a.b": true, "https://a.b": false, "": false, "http": false} {
		if got := IsPlainHTTP(in); got != want {
			t.Errorf("IsPlainHTTP(%q) = %v, want %v", in, got, want)
		}
	}
}
