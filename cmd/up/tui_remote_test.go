package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/heohlee/up/internal/client"
	"github.com/heohlee/up/internal/connection"
	"github.com/heohlee/up/internal/store"
	"github.com/heohlee/up/internal/tui"
	"testing"
)

func TestRemoteTUIFlagValidation(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		args      []string
		code      int
		stderr    string
	}{
		{"missing", "", []string{"--remote"}, 1, "up: no saved remote; run 'up login'\n"},
		{"env", "true", nil, 1, "up: no saved remote; run 'up login'\n"},
		{"invalid", "x", nil, 1, "up: UP_REMOTE: invalid value \"x\" (use true or false)\n"},
		{"db", "", []string{"--remote", "--db", "x"}, 1, "up: --db cannot be used with --remote\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateDefaultDatabase(t)
			t.Setenv("UP_REMOTE", tc.env)
			args := append([]string{"tui"}, tc.args...)
			code, out, errOut := runCmd(context.Background(), args...)
			if code != tc.code || out != "" || errOut != tc.stderr {
				t.Fatalf("got %d %q %q", code, out, errOut)
			}
		})
	}
}

func TestRemoteTUIRunnerAndIsolation(t *testing.T) {
	isolateDefaultDatabase(t)
	t.Setenv("UP_REMOTE", "")
	t.Chdir(t.TempDir())
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"server_time":"2026-01-01T00:00:00Z"}`))
	}))
	defer server.Close()
	if err := connection.SaveProfile(connection.Profile{ServerURL: server.URL, Token: "secret-token"}); err != nil {
		t.Fatal(err)
	}
	prevRemote, prevLocal := runRemoteTUIProgram, runTUIProgram
	t.Cleanup(func() { runRemoteTUIProgram, runTUIProgram = prevRemote, prevLocal })
	runTUIProgram = func(context.Context, *store.Store, ...tui.Options) error { t.Fatal("local runner called"); return nil }
	runRemoteTUIProgram = func(ctx context.Context, s tui.Store, opts ...tui.Options) error {
		c, ok := s.(*client.Client)
		if !ok {
			t.Fatalf("store type %T", s)
		}
		if len(opts) != 1 || opts[0].Destination != server.URL || opts[0].Hostname != strings.TrimPrefix(server.URL, "http://") {
			t.Fatalf("options %+v", opts)
		}
		_, err := c.LoadSnapshot(ctx, time.Now())
		return err
	}
	t.Setenv("UP_DB", filepath.Join(t.TempDir(), "ignored.db"))
	for _, tc := range []struct {
		env  string
		args []string
	}{{"", []string{"--remote"}}, {"true", nil}} {
		t.Setenv("UP_REMOTE", tc.env)
		code, out, errOut := runCmd(context.Background(), append([]string{"tui"}, tc.args...)...)
		if code != 0 || out != "" || errOut != "" || auth != "Bearer secret-token" {
			t.Fatalf("got %d %q %q auth=%q", code, out, errOut, auth)
		}
	}
	entries, err := os.ReadDir(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "up"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "remote.json" {
		t.Fatalf("entries %v err %v", entries, err)
	}
	if _, err := os.Stat(os.Getenv("UP_DB")); !os.IsNotExist(err) {
		t.Fatalf("env db touched: %v", err)
	}
	runRemoteTUIProgram = func(context.Context, tui.Store, ...tui.Options) error { return errors.New("runner failed") }
	code, out, errOut := runCmd(context.Background(), "tui", "--remote")
	if code != 1 || out != "" || errOut != "up: runner failed\n" {
		t.Fatalf("got %d %q %q", code, out, errOut)
	}
}

func TestRemoteTUIHelp(t *testing.T) {
	isolateDefaultDatabase(t)
	t.Setenv("UP_REMOTE", "")
	code, out, errOut := runCmd(context.Background(), "tui", "-h")
	if code != 0 || errOut != "" {
		t.Fatalf("got %d %q", code, errOut)
	}
	for _, want := range []string{"--remote", "UP_REMOTE", "up login", "r retry"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}
