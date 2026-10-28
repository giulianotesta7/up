package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

// schemaV1 is the schema written by up v0.4.0 and earlier. It is frozen here
// so the migration is always tested against the real previous layout.
const schemaV1 = `
CREATE TABLE checks (
	position        INTEGER NOT NULL,
	name            TEXT    NOT NULL PRIMARY KEY,
	url             TEXT    NOT NULL,
	interval_ns     INTEGER NOT NULL,
	timeout_ns      INTEGER NOT NULL,
	expected_status INTEGER NOT NULL
);
CREATE TABLE results (
	id          INTEGER PRIMARY KEY,
	check_name  TEXT    NOT NULL,
	at_ns       INTEGER NOT NULL,
	status      TEXT    NOT NULL CHECK (status IN ('up', 'down')),
	latency_ns  INTEGER NOT NULL,
	status_code INTEGER NOT NULL,
	error       TEXT    NOT NULL
);
CREATE INDEX results_by_check_time ON results (check_name, at_ns);
CREATE TABLE daemon (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	last_beat_ns INTEGER NOT NULL
);
PRAGMA user_version = 1;
`

// v1Database writes a schema v1 database with two monitors, history and a
// heartbeat, the way up v0.4.0 left it.
func v1Database(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "up.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
INSERT INTO checks VALUES (0, 'api', 'https://api.example.com', 30000000000, 5000000000, 200);
INSERT INTO checks VALUES (1, 'web', 'https://example.com', 60000000000, 10000000000, 204);
INSERT INTO daemon VALUES (1, ?);`, t0.UnixNano()); err != nil {
		t.Fatal(err)
	}
	for _, r := range []check.Result{result("api", 0, check.StatusUp), result("api", 1, check.StatusDown), result("web", 2, check.StatusUp)} {
		if _, err := db.Exec("INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error) VALUES (?, ?, ?, ?, ?, ?)",
			r.CheckName, r.At.UnixNano(), string(r.Status), int64(r.Latency), r.StatusCode, r.Error); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func version(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := userVersion(db)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func tables(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

var v2Tables = []string{"channels", "checks", "daemon", "results", "secret_key", "smtp_servers"}

func TestOpenMigratesV1AndKeepsData(t *testing.T) {
	path := v1Database(t)
	ctx := context.Background()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v1) error = %v", err)
	}
	defer s.Close()

	if got := version(t, path); got != 3 {
		t.Fatalf("user_version after Open = %d, want 3", got)
	}
	if got := tables(t, path); !reflect.DeepEqual(got, v2Tables) {
		t.Fatalf("tables after migration = %v, want %v", got, v2Tables)
	}
	checks, err := s.Checks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.Check{
		{Name: "api", URL: "https://api.example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200},
		{Name: "web", URL: "https://example.com", Interval: time.Minute, Timeout: 10 * time.Second, ExpectedStatus: 204},
	}
	if !reflect.DeepEqual(checks, want) {
		t.Fatalf("Checks() after migration = %+v, want %+v", checks, want)
	}
	recent, err := s.Recent(ctx, "api", 10)
	if err != nil {
		t.Fatal(err)
	}
	if wantRecent := []check.Result{result("api", 1, check.StatusDown), result("api", 0, check.StatusUp)}; !reflect.DeepEqual(recent, wantRecent) {
		t.Fatalf("Recent(api) after migration = %+v, want %+v", recent, wantRecent)
	}
	beat, ok, err := s.LastBeat(ctx)
	if err != nil || !ok || !beat.Equal(t0) {
		t.Fatalf("LastBeat() after migration = %v, %v, %v, want %v", beat, ok, err, t0)
	}

	// Migrated monitors keep accepting results for their unchanged definition.
	stored, err := s.RecordDefined(ctx, want[1], result("web", 3, check.StatusDown))
	if err != nil || !stored {
		t.Fatalf("RecordDefined() after migration = %v, %v, want stored", stored, err)
	}
}

func TestOpenCreatesV2AndReopenIsIdempotent(t *testing.T) {
	s, path := openTemp(t)
	record(t, s, result("api", 0, check.StatusUp))
	s.Close()
	if got := version(t, path); got != 3 {
		t.Fatalf("user_version of a new database = %d, want 3", got)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer again.Close()
	if got := tables(t, path); !reflect.DeepEqual(got, v2Tables) {
		t.Fatalf("tables after reopen = %v, want %v", got, v2Tables)
	}
	if _, ok, err := again.Latest(context.Background(), "api"); !ok || err != nil {
		t.Fatalf("Latest() after reopen = %v, %v, want the stored result", ok, err)
	}
}

func TestOpenReadOnlyRejectsV1WithUpgradeHint(t *testing.T) {
	path := v1Database(t)

	_, err := OpenReadOnly(path)
	if err == nil || !strings.Contains(err.Error(), "database schema version 1 is older than this up (3); open it with `up serve` or `up tui` to upgrade it") {
		t.Fatalf("OpenReadOnly(v1) error = %v, want upgrade hint", err)
	}
	if got := version(t, path); got != 1 {
		t.Fatalf("user_version after read-only open = %d, want 1 (unchanged)", got)
	}
}
