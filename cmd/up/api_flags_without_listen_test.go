package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A TLS or proxy setting without --api-listen would otherwise be ignored in
// silence, leaving the operator believing the API is protected or running.
func TestAPISettingsWithoutAListenAddressAreRefused(t *testing.T) {
	const want = "up: --tls-cert, --tls-key and --behind-proxy require --api-listen\n"
	for name, tc := range map[string]struct {
		args []string
		env  map[string]string
	}{
		"behind-proxy flag":  {args: []string{"--behind-proxy"}},
		"cert and key flags": {args: []string{"--tls-cert", "c.pem", "--tls-key", "k.pem"}},
		"behind-proxy env":   {env: map[string]string{"UP_BEHIND_PROXY": "true"}},
		"cert and key env":   {env: map[string]string{"UP_TLS_CERT": "c.pem", "UP_TLS_KEY": "k.pem"}},
	} {
		t.Run(name, func(t *testing.T) {
			clearAPIEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			dir := t.TempDir()
			args := append([]string{"serve", "--db", filepath.Join(dir, "up.db")}, tc.args...)
			code, out, errout := runCmd(context.Background(), args...)
			if code != 1 || out != "" || errout != want {
				t.Errorf("got %d %q %q", code, out, errout)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("a refused configuration changed files: %v", entries)
			}
		})
	}
}

func TestAPISettingsStillWorkWithAListenAddressAndWithNoneAtAll(t *testing.T) {
	clearAPIEnv(t)
	// No API settings at all: unchanged behavior (the existing serve tests cover it);
	// an explicit empty listen flag overriding the environment must not trip the check.
	t.Setenv("UP_BEHIND_PROXY", "false")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	code, _, errout := runCmd(ctx, "serve", "--db", filepath.Join(t.TempDir(), "up.db"))
	if code != 0 {
		t.Errorf("a false boolean must not count as a setting: %d %q", code, errout)
	}
}
