package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
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
	"github.com/giulianotesta7/up/internal/secret"
	"github.com/giulianotesta7/up/internal/store"
)

// A stop requested while serve is still starting is a clean shutdown, not an
// error: the startup reads that take the context must not turn it into exit 1.
func TestServeStopsCleanlyWhenCanceledDuringStartup(t *testing.T) {
	isolateDefaultDatabase(t)
	path := filepath.Join(t.TempDir(), "up.db")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, out, errOut := runCmd(ctx, "serve", "--db", path)
	if code != 0 || out != "" || strings.Contains(errOut, "up:") {
		t.Fatalf("serve = %d, stdout %q, stderr %q; want a clean stop", code, out, errOut)
	}
	if _, err := os.Stat(path + ".key"); err != nil {
		t.Fatalf("key not created: %v", err)
	}
}

func TestServeCreatesKey(t *testing.T) {
	isolateDefaultDatabase(t)
	path := filepath.Join(t.TempDir(), "up.db")
	// Stop serve once startup has stored the public key, not after a fixed
	// delay: startup time depends on the machine and the migrations it runs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type exit struct {
		code   int
		errOut string
	}
	done := make(chan exit, 1)
	go func() { code, _, errOut := runCmd(ctx, "serve", "--db", path); done <- exit{code, errOut} }()
	deadline := time.Now().Add(5 * time.Second)
	for stored := false; !stored; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("serve did not store the public key within 5s")
		}
		if s, err := store.OpenReadOnly(path); err == nil {
			_, stored, _ = s.PublicKey(context.Background())
			s.Close()
		}
	}
	cancel()
	if got := <-done; got.code != 0 {
		t.Fatalf("%d %s", got.code, got.errOut)
	}
	info, err := os.Stat(path + ".key")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key stat %v %v", info, err)
	}
	key, created, err := secret.LoadOrCreate(path + ".key")
	if err != nil || created {
		t.Fatalf("key %v %v", created, err)
	}
	s, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	public, ok, err := s.PublicKey(context.Background())
	if err != nil || !ok || !bytes.Equal(public, key.PublicKey()) {
		t.Fatalf("public key mismatch: %v %v", ok, err)
	}
}
func TestServeRefusesInsecureKey(t *testing.T) {
	isolateDefaultDatabase(t)
	path := filepath.Join(t.TempDir(), "up.db")
	keyPath := path + ".key"
	original := []byte("unchanged")
	if err := os.WriteFile(keyPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runCmd(context.Background(), "serve", "--db", path)
	want := fmt.Sprintf("up: key file %s is accessible by group or others (run: chmod 600 %s)\n", keyPath, keyPath)
	if code != 1 || out != "" || errOut != want {
		t.Fatalf("%d %q %q", code, out, errOut)
	}
	data, err := os.ReadFile(keyPath)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatalf("key changed: %q %v", data, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused start created the database: %v", err)
	}
}

func TestServeAlertHelp(t *testing.T) {
	isolateDefaultDatabase(t)
	code, out, errOut := runCmd(context.Background(), "serve", "-h")
	if code != 0 || errOut != "" {
		t.Fatalf("%d %q", code, errOut)
	}
	for _, text := range []string{"DOWN", "recovers", "SMTP servers", "<db>.key", "0600", "back it up", "group or others"} {
		if !strings.Contains(out, text) {
			t.Errorf("missing %q", text)
		}
	}
}

func TestServeRunsSQLiteDefinitionsAndPreservesHistory(t *testing.T) {
	isolateDefaultDatabase(t)
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
	isolateDefaultDatabase(t)
	code, out, errOut := runCmd(context.Background(), "serve", "--help")
	if code != 0 || errOut != "" {
		t.Fatalf("got %d, %q", code, errOut)
	}
	for _, text := range []string{"SQLite", "without restarting", "up tui",
		"ntfy, telegram, discord, slack and gotify channels send the alert text to their\nservice; a 2xx answer is delivered, with no redirects and no retries."} {
		if !strings.Contains(out, text) {
			t.Errorf("help missing %q: %s", text, out)
		}
	}
}

func TestServeRetentionPrunesOldResults(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		args      []string
		count     int
	}{
		{"default absent", "absent", nil, 4},
		{"default empty", "", nil, 4},
		{"env one hour", "1h", nil, 2},
		{"env zero", "0", nil, 5},
		{"flag", "", []string{"--retention", "1h"}, 2},
		{"override invalid", "broken", []string{"--retention", "1h"}, 2},
		{"override negative", "-1h", []string{"--retention", "1h"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateDefaultDatabase(t)
			clearAPIEnv(t)
			t.Setenv("UP_RETENTION", tc.env)
			if tc.env == "absent" {
				if err := os.Unsetenv("UP_RETENTION"); err != nil {
					t.Fatal(err)
				}
			}
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
			for _, ago := range []time.Duration{32 * 24 * time.Hour, 31 * 24 * time.Hour, 150 * time.Minute, 2 * time.Hour} {
				if err := s.Record(bg, check.Result{CheckName: "api", At: time.Now().Add(-ago), Status: check.StatusUp}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(bg, time.Second)
			defer cancel()
			args := append([]string{"serve", "--db", dbPath}, tc.args...)
			if code, _, errOut := runCmd(ctx, args...); code != 0 {
				t.Fatalf("serve exit %d: %s", code, errOut)
			}
			got, _ := s.Recent(bg, "api", 100)
			if len(got) != tc.count || got[0].Status != check.StatusDown || got[1].Status != check.StatusUp {
				t.Fatalf("after retention got %+v, want %d results", got, tc.count)
			}
		})
	}
}

func TestServeRejectsNegativeRetention(t *testing.T) {
	isolateDefaultDatabase(t)
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
	isolateDefaultDatabase(t)
	_, out, _ := runCmd(context.Background(), "serve", "--help")
	for _, text := range []string{"-retention", "720h", "0 keeps"} {
		if !strings.Contains(out, text) {
			t.Errorf("help missing %q: %s", text, out)
		}
	}
}

func TestServeNoLongerAcceptsConfig(t *testing.T) {
	isolateDefaultDatabase(t)
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

func TestServeInsecureKeyLeavesV1DatabaseUntouched(t *testing.T) {
	isolateDefaultDatabase(t)
	path := filepath.Join(t.TempDir(), "up.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE checks (position INTEGER NOT NULL, name TEXT NOT NULL PRIMARY KEY, url TEXT NOT NULL, interval_ns INTEGER NOT NULL, timeout_ns INTEGER NOT NULL, expected_status INTEGER NOT NULL);
CREATE TABLE results (id INTEGER PRIMARY KEY, check_name TEXT NOT NULL, at_ns INTEGER NOT NULL, status TEXT NOT NULL CHECK (status IN ('up', 'down')), latency_ns INTEGER NOT NULL, status_code INTEGER NOT NULL, error TEXT NOT NULL);
CREATE INDEX results_by_check_time ON results (check_name, at_ns);
CREATE TABLE daemon (id INTEGER PRIMARY KEY CHECK (id = 1), last_beat_ns INTEGER NOT NULL);
INSERT INTO checks VALUES (0, 'api', 'https://api.example.com', 30000000000, 5000000000, 200);
PRAGMA user_version = 1;`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := path + ".key"
	if err := os.WriteFile(keyPath, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0640); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCmd(context.Background(), "serve", "--db", path)
	want := fmt.Sprintf("up: key file %s is accessible by group or others (run: chmod 600 %s)\n", keyPath, keyPath)
	if code != 1 || out != "" || errOut != want {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected start changed the v1 database (err %v)", err)
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			t.Fatalf("rejected start created %s", sidecar)
		}
	}
}

// The daemon sends a requested channel test and records the outcome without
// touching monitor data. The SMTP server is a closed local port, so no
// listener is needed and the test ends as an error.
func TestServeProcessesChannelTest(t *testing.T) {
	isolateDefaultDatabase(t)
	path := filepath.Join(t.TempDir(), "up.db")
	ctx := context.Background()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddSMTPServer(ctx, config.SMTPServer{Name: "smtp", Host: "127.0.0.1", Port: 1, TLS: config.TLSNone, Sender: "up@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddChannel(ctx, config.Channel{Name: "mail", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"a@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnqueueChannelTest(ctx, "mail", time.Now()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	run, cancel := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() { code, _, _ := runCmd(run, "serve", "--db", path); done <- code }()
	reader, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	deadline := time.Now().Add(10 * time.Second)
	var got config.ChannelTest
	for time.Now().Before(deadline) {
		if got, _, err = reader.LatestChannelTest(ctx, "mail"); err == nil && got.Status == config.TestError {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("serve exited %d", code)
	}
	if got.Status != config.TestError || got.Error == "" {
		t.Fatalf("test = %+v, want a processed error", got)
	}
}

func TestServeHelpMentionsChannelTests(t *testing.T) {
	isolateDefaultDatabase(t)
	if code, out, _ := runCmd(context.Background(), "serve", "-h"); code != 0 || !strings.Contains(out, "Channel tests") {
		t.Fatalf("%d %q", code, out)
	}
}

// The daemon opens sealed request headers and body with the key serve loads.
func TestServeOpensSealedRequestSettings(t *testing.T) {
	isolateDefaultDatabase(t)
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "up.db")
	type seen struct{ method, token, body string }
	got := make(chan seen, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		select {
		case got <- seen{r.Method, r.Header.Get("X-Token"), b.String()}:
		default:
		}
	}))
	defer srv.Close()
	key, _, err := secret.LoadOrCreate(dbPath + ".key")
	if err != nil {
		t.Fatal(err)
	}
	seal := func(v string) secret.Sealed {
		s, err := secret.Seal(key.PublicKey(), v)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := config.Check{Name: "api", URL: srv.URL, Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200,
		Method: "POST", Headers: []config.Header{{Name: "X-Token", Value: seal("s3cret")}}, Body: seal("payload")}
	if err := s.AddCheck(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var errOut string
	go func() { defer close(done); _, _, errOut = runCmd(ctx, "serve", "--db", dbPath) }()
	defer func() { cancel(); <-done }()
	select {
	case v := <-got:
		if v != (seen{"POST", "s3cret", "payload"}) {
			t.Fatalf("server saw %+v", v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon never sent the request")
	}
	cancel()
	<-done
	if strings.Contains(errOut, "s3cret") || strings.Contains(errOut, "payload") {
		t.Fatalf("protected value logged: %s", errOut)
	}
}
