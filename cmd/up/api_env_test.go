package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearAPIEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"UP_API_LISTEN", "UP_TLS_CERT", "UP_TLS_KEY", "UP_BEHIND_PROXY", "UP_NO_TLS"} {
		t.Setenv(name, "")
	}
}

func TestAPIEnvironmentVariablesMatchFlags(t *testing.T) {
	unsafe := "up: API address \"0.0.0.0:12345\" is reachable without TLS: add --tls-cert and --tls-key, use --behind-proxy behind a TLS proxy, use --no-tls to accept plain HTTP, or listen on 127.0.0.1\n"
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"listen address is validated": {map[string]string{"UP_API_LISTEN": "0.0.0.0:12345"}, unsafe},
		"cert without key":            {map[string]string{"UP_API_LISTEN": "127.0.0.1:0", "UP_TLS_CERT": "c"}, "up: --tls-cert and --tls-key must be provided together\n"},
		"key without cert":            {map[string]string{"UP_API_LISTEN": "127.0.0.1:0", "UP_TLS_KEY": "k"}, "up: --tls-cert and --tls-key must be provided together\n"},
		"no-tls needs a listener":     {map[string]string{"UP_NO_TLS": "true"}, "up: --no-tls requires --api-listen\n"},
		"no-tls conflicts with proxy": {map[string]string{"UP_API_LISTEN": "127.0.0.1:0", "UP_NO_TLS": "1", "UP_BEHIND_PROXY": "true"}, "up: --no-tls cannot be combined with --tls-cert, --tls-key or --behind-proxy\n"},
		"invalid boolean":             {map[string]string{"UP_API_LISTEN": "127.0.0.1:0", "UP_NO_TLS": "maybe"}, "up: UP_NO_TLS: invalid value \"maybe\" (use true or false)\n"},
		"invalid proxy boolean":       {map[string]string{"UP_API_LISTEN": "127.0.0.1:0", "UP_BEHIND_PROXY": "si"}, "up: UP_BEHIND_PROXY: invalid value \"si\" (use true or false)\n"},
	} {
		t.Run(name, func(t *testing.T) {
			clearAPIEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			dir := t.TempDir()
			code, out, errout := runCmd(context.Background(), "serve", "--db", filepath.Join(dir, "up.db"))
			if code != 1 || out != "" || errout != tc.want {
				t.Errorf("got %d %q %q; want 1 %q", code, out, errout, tc.want)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("rejected configuration changed files: %v", entries)
			}
		})
	}
}

func TestAPIEnvironmentListenStartsTheAPI(t *testing.T) {
	clearAPIEnv(t)
	t.Setenv("UP_API_LISTEN", "127.0.0.1:0")
	path := filepath.Join(t.TempDir(), "up.db")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	code, _, errout := runCmd(ctx, "serve", "--db", path)
	if code != 0 || !strings.Contains(errout, "up: API listening on 127.0.0.1:") || !strings.Contains(errout, "up: access token (shown once): up_") {
		t.Fatalf("got %d %q", code, errout)
	}
	if info, err := os.Stat(path + ".token"); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("token file: %v %v", info, err)
	}
}

func TestAPIEnvironmentNoTLSWarns(t *testing.T) {
	clearAPIEnv(t)
	t.Setenv("UP_API_LISTEN", "0.0.0.0:0")
	t.Setenv("UP_NO_TLS", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	code, _, errout := runCmd(ctx, "serve", "--db", filepath.Join(t.TempDir(), "up.db"))
	if code != 0 || !strings.Contains(errout, "up: WARNING: API over plain HTTP") {
		t.Fatalf("got %d %q", code, errout)
	}
}

func TestExplicitFlagsOverrideTheEnvironment(t *testing.T) {
	clearAPIEnv(t)
	t.Setenv("UP_API_LISTEN", "0.0.0.0:12345") // refused on its own
	t.Setenv("UP_NO_TLS", "maybe")             // invalid on its own
	path := filepath.Join(t.TempDir(), "up.db")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	code, _, errout := runCmd(ctx, "serve", "--db", path, "--api-listen", "", "--no-tls=false")
	if code != 0 || strings.Contains(errout, "API listening") || strings.Contains(errout, "UP_NO_TLS") {
		t.Fatalf("flags must win over the environment: %d %q", code, errout)
	}
	if _, err := os.Stat(path + ".token"); !os.IsNotExist(err) {
		t.Errorf("no API means no token file: %v", err)
	}
}

func TestServeHelpNamesTheEnvironmentVariables(t *testing.T) {
	code, out, _ := runCmd(context.Background(), "serve", "-h")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, name := range []string{"UP_API_LISTEN", "UP_TLS_CERT", "UP_TLS_KEY", "UP_BEHIND_PROXY", "UP_NO_TLS"} {
		if !strings.Contains(out, name) {
			t.Errorf("serve -h does not mention %s", name)
		}
	}
}
