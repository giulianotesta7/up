package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/store"
)

func TestServeRunsSQLiteDefinitionsAndPreservesHistory(t *testing.T) {
	for _, mode := range []string{"empty", "preexisting"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			dbPath := filepath.Join(dir, "up.db")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
			}))
			defer srv.Close()
			s, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			bg := context.Background()
			existing := config.Check{Name: "existing", URL: srv.URL, Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200}
			history := check.Result{CheckName: "existing", At: time.Now().Add(-time.Hour), Status: check.StatusDown, Error: "old failure"}
			if mode == "preexisting" {
				if err := s.AddCheck(bg, existing); err != nil {
					t.Fatal(err)
				}
				if err := s.Record(bg, history); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(bg)
			done := make(chan struct{})
			var code int
			var out, errOut string
			go func() {
				defer close(done)
				code, out, errOut = runCmd(ctx, "serve", "--db", dbPath)
			}()
			defer func() { cancel(); <-done }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				_, beat, _ := s.LastBeat(bg)
				results, _ := s.Recent(bg, "existing", 100)
				if beat && (mode == "empty" || len(results) >= 2) {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("daemon did not run SQLite definitions / heartbeat")
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			<-done
			if code != 0 || out != "" || !strings.Contains(errOut, "monitoring stopped") {
				t.Fatalf("got %d, %q, %q", code, out, errOut)
			}
			definitions, _ := s.Checks(bg)
			var want []config.Check
			if mode == "preexisting" {
				want = []config.Check{existing}
			}
			if !reflect.DeepEqual(definitions, want) {
				t.Fatalf("definitions = %v, want %v", definitions, want)
			}
			if mode == "preexisting" {
				results, _ := s.Recent(bg, "existing", 100)
				if len(results) != 2 || results[1].Error != history.Error {
					t.Fatalf("history not preserved: %v", results)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "up.yaml")); !os.IsNotExist(err) {
				t.Fatal("serve created or expected a YAML file")
			}
		})
	}
}

func TestServeHelpDescribesSQLite(t *testing.T) {
	code, out, errOut := runCmd(context.Background(), "serve", "--help")
	if code != 0 || errOut != "" {
		t.Fatalf("got %d, %q", code, errOut)
	}
	for _, text := range []string{"SQLite", "without restarting", "up tui"} {
		if !strings.Contains(out, text) {
			t.Errorf("help missing %q: %s", text, out)
		}
	}
}

func TestServeRetentionPrunesOldResults(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "up.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bg := context.Background()
	c := config.Check{Name: "api", URL: "http://127.0.0.1:1", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200}
	if err := s.AddCheck(bg, c); err != nil {
		t.Fatal(err)
	}
	for _, ago := range []time.Duration{3 * time.Hour, 150 * time.Minute, 2 * time.Hour} {
		if err := s.Record(bg, check.Result{CheckName: "api", At: time.Now().Add(-ago), Status: check.StatusUp}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(bg, 500*time.Millisecond)
	defer cancel()
	if code, _, errOut := runCmd(ctx, "serve", "--db", dbPath, "--retention", "1h"); code != 0 {
		t.Fatalf("serve exit %d: %s", code, errOut)
	}
	got, _ := s.Recent(bg, "api", 100)
	if len(got) != 2 || got[0].Status != check.StatusDown || got[1].Status != check.StatusUp {
		t.Fatalf("after retention got %+v, want the fresh check plus one boundary result", got)
	}
}

func TestServeRejectsNegativeRetention(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "up.db")
	code, out, errOut := runCmd(context.Background(), "serve", "--db", dbPath, "--retention", "-1h")
	if code != 1 || out != "" || errOut != "up: --retention must not be negative\n" {
		t.Fatalf("got %d, %q, %q", code, out, errOut)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("database was created despite invalid retention")
	}
}

func TestServeHelpDescribesRetention(t *testing.T) {
	_, out, _ := runCmd(context.Background(), "serve", "--help")
	for _, text := range []string{"-retention", "720h", "0 keeps"} {
		if !strings.Contains(out, text) {
			t.Errorf("help missing %q: %s", text, out)
		}
	}
}

func TestServeNoLongerAcceptsConfig(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "up.db")
	code, out, errOut := runCmd(context.Background(), "serve", "--db", dbPath, "--config", filepath.Join(dir, "monitors.yaml"))
	if code != 2 || out != "" || !strings.Contains(errOut, "flag provided but not defined: -config") {
		t.Fatalf("got %d, %q, %q", code, out, errOut)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("database was created for a rejected flag")
	}
	_, help, _ := runCmd(context.Background(), "serve", "--help")
	if strings.Contains(strings.ToLower(help), "yaml") {
		t.Fatalf("help still mentions YAML: %s", help)
	}
}
