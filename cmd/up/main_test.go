package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/store"
)

func runCmd(ctx context.Context, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(ctx, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		code, stdout, stderr := runCmd(context.Background(), args...)
		if code != 0 || stdout != usage || stderr != "" {
			t.Errorf("up %v = %d, stdout %q, stderr %q; want 0 with usage on stdout", args, code, stdout, stderr)
		}
	}

	code, stdout, stderr := runCmd(context.Background())
	if code != 2 || stdout != "" || stderr != usage {
		t.Errorf("up = %d, stdout %q, stderr %q; want 2 with usage on stderr", code, stdout, stderr)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, stdout, stderr := runCmd(context.Background(), "bogus")

	if code != 2 || stdout != "" || stderr != "up: unknown command \"bogus\"\n\n"+usage {
		t.Fatalf("up bogus = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestServeHelp(t *testing.T) {
	code, stdout, stderr := runCmd(context.Background(), "serve", "-h")

	if code != 0 || stderr != "" {
		t.Fatalf("up serve -h = %d, stderr %q; want 0", code, stderr)
	}
	for _, want := range []string{"Usage: up serve [flags]", "-config", "-db"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("up serve -h stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestServeRejectsBadFlag(t *testing.T) {
	code, stdout, stderr := runCmd(context.Background(), "serve", "--nope")

	if code != 2 || stdout != "" || !strings.Contains(stderr, "flag provided but not defined: -nope") || !strings.Contains(stderr, "Usage: up serve [flags]") {
		t.Fatalf("up serve --nope = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestServeRejectsInvalidConfigWithoutStarting(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "up.yaml")
	dbPath := filepath.Join(dir, "up.db")
	if err := os.WriteFile(cfgPath, []byte("checks:\n  - name: api\n    url: ftp://example.com\n    interval: 30s\n    timeout: 5s\n    expected_status: 200\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCmd(context.Background(), "serve", "--config", cfgPath, "--db", dbPath)

	if code != 1 || stdout != "" {
		t.Fatalf("up serve = %d, stdout %q; want 1", code, stdout)
	}
	if want := "up: config: checks[0] (api): url \"ftp://example.com\" must use http or https\n"; stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("database was created for an invalid config (stat error %v)", err)
	}
}

func TestServeMissingConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "missing.yaml")

	code, _, stderr := runCmd(context.Background(), "serve", "--config", cfgPath, "--db", filepath.Join(dir, "up.db"))

	if code != 1 || !strings.HasPrefix(stderr, "up: config: read "+cfgPath) {
		t.Fatalf("up serve = %d, stderr %q; want 1 with a read error", code, stderr)
	}
}

func TestServeRecordsResultsUntilCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "up.yaml")
	dbPath := filepath.Join(dir, "up.db")
	cfg := "checks:\n  - name: api\n    url: " + srv.URL + "\n    interval: 50ms\n    timeout: 50ms\n    expected_status: 200\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		code           int
		stdout, stderr string
	}
	done := make(chan outcome, 1)
	go func() {
		code, stdout, stderr := runCmd(ctx, "serve", "--config", cfgPath, "--db", dbPath)
		done <- outcome{code, stdout, stderr}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if s, err := store.OpenReadOnly(dbPath); err == nil {
			recent, _ := s.Recent(context.Background(), "api", 2)
			s.Close()
			if len(recent) == 2 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("serve did not record two results")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case got := <-done:
		if got.code != 0 || got.stdout != "" {
			t.Fatalf("up serve = %d, stdout %q; want 0", got.code, got.stdout)
		}
		for _, want := range []string{"monitoring started", "check up", "monitoring stopped"} {
			if !strings.Contains(got.stderr, want) {
				t.Errorf("stderr = %q, want it to log %q", got.stderr, want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after cancel")
	}
}

func TestTUIHelp(t *testing.T) {
	code, stdout, stderr := runCmd(context.Background(), "tui", "-h")

	if code != 0 || stderr != "" {
		t.Fatalf("up tui -h = %d, stderr %q; want 0", code, stderr)
	}
	for _, want := range []string{"Usage: up tui [flags]", "-db"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("up tui -h stdout = %q, want it to contain %q", stdout, want)
		}
	}
}

func TestTUIRejectsBadFlag(t *testing.T) {
	code, stdout, stderr := runCmd(context.Background(), "tui", "--nope")

	if code != 2 || stdout != "" || !strings.Contains(stderr, "flag provided but not defined: -nope") || !strings.Contains(stderr, "Usage: up tui [flags]") {
		t.Fatalf("up tui --nope = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestTUIMissingDatabaseCreatesIt(t *testing.T) {
	t.Chdir(t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "up.db")
	previous := runTUIProgram
	t.Cleanup(func() { runTUIProgram = previous })
	started := false
	runTUIProgram = func(ctx context.Context, s *store.Store) error {
		started = true
		checks, err := s.Checks(ctx)
		if err != nil || len(checks) != 0 {
			t.Fatalf("first-use checks = %v, %v", checks, err)
		}
		return nil
	}
	code, stdout, stderr := runCmd(context.Background(), "tui", "--db", dbPath)
	if code != 0 || stdout != "" || stderr != "" || !started {
		t.Fatalf("up tui = %d, stdout %q, stderr %q, started %v", code, stdout, stderr, started)
	}
	s, err := store.OpenReadOnly(dbPath)
	if err != nil {
		t.Fatalf("first-use database not initialized: %v", err)
	}
	s.Close()
}
