package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAPISettingsWithoutAListenAddressAreRefused(t *testing.T) {
	const want = "up: --tls-cert and --tls-key require --api-listen\n"
	for name, tc := range map[string]struct {
		args []string
		env  map[string]string
	}{
		"flags": {args: []string{"--tls-cert", "c.pem", "--tls-key", "k.pem"}},
		"env":   {env: map[string]string{"UP_TLS_CERT": "c.pem", "UP_TLS_KEY": "k.pem"}},
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
				t.Errorf("changed files: %v", entries)
			}
		})
	}
}
