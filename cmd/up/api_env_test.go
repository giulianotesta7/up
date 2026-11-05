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
	for _, name := range []string{"UP_API_LISTEN", "UP_TLS_CERT", "UP_TLS_KEY", "UP_BEHIND_PROXY", "UP_NO_TLS", "UP_RETENTION"} {
		t.Setenv(name, "")
	}
}

func TestAPIEnvironmentVariablesMatchFlags(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"invalid listen":   {map[string]string{"UP_API_LISTEN": "bad"}, "up: invalid API address \"bad\": address bad: missing port in address\n"},
		"cert without key": {map[string]string{"UP_API_LISTEN": "127.0.0.1:0", "UP_TLS_CERT": "c"}, "up: --tls-cert and --tls-key must be provided together\n"},
		"key without cert": {map[string]string{"UP_API_LISTEN": "127.0.0.1:0", "UP_TLS_KEY": "k"}, "up: --tls-cert and --tls-key must be provided together\n"},
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
				t.Errorf("changed files: %v", entries)
			}
		})
	}
}

func TestAPIEnvironmentListenStartsTheAPI(t *testing.T) {
	clearAPIEnv(t)
	t.Setenv("UP_API_LISTEN", "0.0.0.0:0")
	t.Setenv("UP_NO_TLS", "invalid")
	t.Setenv("UP_BEHIND_PROXY", "invalid")
	path := filepath.Join(t.TempDir(), "up.db")
	// Stop serve once the API has started (its token file exists), not after
	// a fixed delay that a slow startup can outlast.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type exit struct {
		code        int
		out, errout string
	}
	done := make(chan exit, 1)
	go func() { code, out, errout := runCmd(ctx, "serve", "--db", path); done <- exit{code, out, errout} }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path + ".token"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the API did not start within 5s")
		}
	}
	// The listening line follows the token file; give it a moment.
	time.Sleep(100 * time.Millisecond)
	cancel()
	got := <-done
	code, out, errout := got.code, got.out, got.errout
	if code != 0 || out != "" || !strings.Contains(errout, "up: API listening on ") || strings.Contains(errout, "WARNING: API over plain HTTP") {
		t.Fatalf("got %d %q %q", code, out, errout)
	}
	if info, err := os.Stat(path + ".token"); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("token file: %v %v", info, err)
	}
}

func TestExplicitFlagsOverrideTheEnvironment(t *testing.T) {
	clearAPIEnv(t)
	t.Setenv("UP_API_LISTEN", "bad")
	t.Setenv("UP_TLS_CERT", "c")
	t.Setenv("UP_TLS_KEY", "k")
	path := filepath.Join(t.TempDir(), "up.db")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	code, _, errout := runCmd(ctx, "serve", "--db", path, "--api-listen", "", "--tls-cert", "", "--tls-key", "")
	if code != 0 || strings.Contains(errout, "API listening") {
		t.Fatalf("flags must win: %d %q", code, errout)
	}
	if _, err := os.Stat(path + ".token"); !os.IsNotExist(err) {
		t.Errorf("unexpected token: %v", err)
	}
}

func TestServeHelpNamesTheEnvironmentVariables(t *testing.T) {
	code, out, errout := runCmd(context.Background(), "serve", "-h")
	if code != 0 || errout != "" {
		t.Fatalf("exit %d %q", code, errout)
	}
	for _, name := range []string{"UP_API_LISTEN", "UP_TLS_CERT", "UP_TLS_KEY", "UP_RETENTION"} {
		if !strings.Contains(out, name) {
			t.Errorf("missing %s", name)
		}
	}
	for _, name := range []string{"UP_NO_TLS", "UP_BEHIND_PROXY", "no-tls", "behind-proxy"} {
		if strings.Contains(out, name) {
			t.Errorf("obsolete %s", name)
		}
	}
}
