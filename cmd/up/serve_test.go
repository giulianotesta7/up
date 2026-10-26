package main

import (
	"context"
	"fmt"
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

func TestServeSQLiteSourceAndOptionalImport(t *testing.T) {
	for _, mode := range []string{"empty", "preexisting", "import", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			dbPath := filepath.Join(dir, "up.db")
			cfgPath := filepath.Join(dir, "optional.yaml")
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
			if mode != "empty" {
				if err := s.AddCheck(bg, existing); err != nil {
					t.Fatal(err)
				}
				if err := s.Record(bg, history); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"serve", "--db", dbPath}
			var yaml string
			if mode == "import" || mode == "conflict" {
				status := 200
				if mode == "conflict" {
					status = 503
				}
				yaml = fmt.Sprintf("checks:\n  - name: added\n    url: %s\n    interval: 1h\n    timeout: 1s\n    expected_status: 200\n  - name: existing\n    url: %s\n    interval: 1h\n    timeout: 1s\n    expected_status: %d\n", srv.URL, srv.URL, status)
				if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--config", cfgPath)
			}
			if mode == "conflict" {
				before, err := s.Recent(bg, "existing", 100)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(bg, 200*time.Millisecond)
				defer cancel()
				code, out, errOut := runCmd(ctx, args...)
				if code != 1 || out != "" || errOut != "up: store: import checks: name \"existing\" conflicts with existing definition\n" {
					t.Fatalf("got %d, %q, %q", code, out, errOut)
				}
				definitions, _ := s.Checks(bg)
				after, _ := s.Recent(bg, "existing", 100)
				_, beat, _ := s.LastBeat(bg)
				if !reflect.DeepEqual(definitions, []config.Check{existing}) || !reflect.DeepEqual(before, after) || beat {
					t.Fatalf("rejected import changed state: %v, %v, beat=%v", definitions, after, beat)
				}
				added, _ := s.Recent(bg, "added", 100)
				if len(added) != 0 {
					t.Fatal("rejected addition ran")
				}
			} else {
				ctx, cancel := context.WithCancel(bg)
				done := make(chan struct{})
				var code int
				var out, errOut string
				go func() {
					defer close(done)
					code, out, errOut = runCmd(ctx, args...)
				}()
				defer func() { cancel(); <-done }()
				deadline := time.Now().Add(5 * time.Second)
				for {
					_, beat, _ := s.LastBeat(bg)
					results, _ := s.Recent(bg, "existing", 100)
					added, ok, _ := s.Latest(bg, "added")
					ready := beat && (mode == "empty" || len(results) >= 2) && (mode != "import" || ok && added.Status == check.StatusUp)
					if ready {
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
				if mode != "empty" {
					want = append(want, existing)
				}
				if mode == "import" {
					added := existing
					added.Name = "added"
					want = append(want, added)
				}
				if !reflect.DeepEqual(definitions, want) {
					t.Fatalf("definitions = %v, want %v", definitions, want)
				}
				if mode != "empty" {
					results, _ := s.Recent(bg, "existing", 100)
					if len(results) != 2 || results[1].Error != history.Error {
						t.Fatalf("history not preserved: %v", results)
					}
				}
			}
			if yaml != "" {
				after, err := os.ReadFile(cfgPath)
				if err != nil || string(after) != yaml {
					t.Fatal("explicit YAML was changed")
				}
			}
		})
	}
}

func TestServeInvalidImportPreservesDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "up.db")
	cfgPath := filepath.Join(dir, "invalid.yaml")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	bg := context.Background()
	c := config.Check{Name: "api", URL: "http://example.invalid", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200}
	if err := s.AddCheck(bg, c); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(bg, check.Result{CheckName: c.Name, At: time.Now(), Status: check.StatusDown, Error: "historic failure"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Beat(bg, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Recent(bg, c.Name, 100)
	beatBefore, _, _ := s.LastBeat(bg)
	yaml := "checks:\n  - name: api\n    url: ftp://example.com\n    interval: 30s\n    timeout: 5s\n    expected_status: 200\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCmd(bg, "serve", "--db", dbPath, "--config", cfgPath)
	if code != 1 || out != "" || errOut != "up: config: checks[0] (api): url \"ftp://example.com\" must use http or https\n" {
		t.Fatalf("got %d, %q, %q", code, out, errOut)
	}
	definitions, _ := s.Checks(bg)
	after, _ := s.Recent(bg, c.Name, 100)
	beatAfter, _, _ := s.LastBeat(bg)
	if !reflect.DeepEqual(definitions, []config.Check{c}) || !reflect.DeepEqual(before, after) || !beatBefore.Equal(beatAfter) {
		t.Fatal("invalid import changed definitions, history or heartbeat")
	}
	contents, err := os.ReadFile(cfgPath)
	if err != nil || string(contents) != yaml {
		t.Fatal("invalid YAML file was changed")
	}
}

func TestServeHelpDescribesSQLiteAndOptionalImport(t *testing.T) {
	code, out, errOut := runCmd(context.Background(), "serve", "--help")
	if code != 0 || errOut != "" {
		t.Fatalf("got %d, %q", code, errOut)
	}
	for _, text := range []string{"SQLite", "without restarting", "optional", "No YAML", "conflict"} {
		if !strings.Contains(out, text) {
			t.Errorf("help missing %q: %s", text, out)
		}
	}
	if strings.Contains(out, `default "up.yaml"`) {
		t.Fatal("config still defaults to up.yaml")
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
