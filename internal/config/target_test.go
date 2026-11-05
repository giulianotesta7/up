package config

import "testing"

func TestCheckTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    Check
		want string
	}{
		{"http", Check{URL: "https://example.com/health"}, "https://example.com/health"},
		{"explicit http", Check{Kind: CheckHTTP, URL: "http://a"}, "http://a"},
		{"tcp host", Check{Kind: CheckTCP, Host: "db.example.com", Port: 5432}, "db.example.com:5432"},
		{"tcp ipv6", Check{Kind: CheckTCP, Host: "::1", Port: 80}, "[::1]:80"},
	} {
		if got := tc.c.Target(); got != tc.want {
			t.Errorf("%s: Target() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
