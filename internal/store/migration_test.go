package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
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

var v2Tables = []string{"alert_state", "certs", "channel_tests", "channels", "checks", "daemon", "daemon_api", "daemon_pings", "defaults", "delivery_attempts", "maintenance_windows", "mutes", "results", "secret_key", "smtp_servers", "sqlite_sequence", "tag_mutes"}

func TestOpenMigratesV1AndKeepsData(t *testing.T) {
	path := v1Database(t)
	ctx := context.Background()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v1) error = %v", err)
	}
	defer s.Close()

	if got := version(t, path); got != schemaVersion {
		t.Fatalf("user_version after Open = %d, want %d", got, schemaVersion)
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
	if got := version(t, path); got != schemaVersion {
		t.Fatalf("user_version of a new database = %d, want %d", got, schemaVersion)
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
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("database schema version 1 is older than this up (%d); open it with `up serve` or `up tui` to upgrade it", schemaVersion)) {
		t.Fatalf("OpenReadOnly(v1) error = %v, want upgrade hint", err)
	}
	if got := version(t, path); got != 1 {
		t.Fatalf("user_version after read-only open = %d, want 1 (unchanged)", got)
	}
}

// v5Database writes a database exactly as the previous release left it, by
// running the frozen migrations 0 to 4, with a monitor, a channel and its
// SMTP server.
func v5Database(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "up.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, m := range migrations[:5] {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 5;
INSERT INTO smtp_servers VALUES ('smtp', 'mail.example.com', 587, 'starttls', 'u', x'0102', x'0304', 'a@example.com');
INSERT INTO channels VALUES ('ops', 'email', 'smtp', 'b@example.com');
INSERT INTO checks (position, name, url, interval_ns, timeout_ns, expected_status, channel, alert_after, recover_after, tls_off) VALUES (0, 'api', 'https://api.example.com', 30000000000, 5000000000, 200, 'ops', 2, 3, 1);`); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenMigratesV5KeepingBehavior(t *testing.T) {
	path := v5Database(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v5) error = %v", err)
	}
	defer s.Close()
	if got := version(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	ctx := context.Background()
	checks, err := s.Checks(ctx)
	want := config.Check{Name: "api", URL: "https://api.example.com", Interval: 30 * time.Second, Timeout: 5 * time.Second, ExpectedStatus: 200, Channel: "ops", AlertAfter: 2, RecoverAfter: 3, TLSOff: true}
	if err != nil || len(checks) != 1 || !reflect.DeepEqual(checks[0], want) {
		t.Fatalf("Checks() = %+v, %v, want %+v", checks, err, want)
	}
	channels, err := s.Channels(ctx)
	wantChannel := config.Channel{Name: "ops", Kind: config.KindEmail, SMTPServer: "smtp", Recipients: []string{"b@example.com"}}
	if err != nil || len(channels) != 1 || !reflect.DeepEqual(channels[0], wantChannel) {
		t.Fatalf("Channels() = %+v, %v, want %+v", channels, err, wantChannel)
	}
	if ok, err := s.RecordDefined(ctx, want, result("api", 0, check.StatusUp)); err != nil || !ok {
		t.Fatalf("RecordDefined(migrated definition) = %v, %v, want stored", ok, err)
	}
	if windows, err := s.AllMaintenanceWindows(ctx); err != nil || len(windows) != 0 {
		t.Fatalf("new windows table = %v, %v", windows, err)
	}
	if tests, err := s.LatestChannelTests(ctx); err != nil || len(tests) != 0 {
		t.Fatalf("new channel tests table = %v, %v", tests, err)
	}
}

func TestOpenReadOnlyRejectsV5AndOpenUpgradesIt(t *testing.T) {
	path := v5Database(t)
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("database schema version 5 is older than this up (%d)", schemaVersion)) {
		t.Fatalf("OpenReadOnly(v5) error = %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly after upgrade: %v", err)
	}
	ro.Close()
}

// v6Database writes a database as the previous release left it (migrations 0
// to 5) with a monitor.
func v6Database(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "up.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, m := range migrations[:6] {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 6;
INSERT INTO checks (position, name, url, interval_ns, timeout_ns, expected_status) VALUES (0, 'api', 'https://api.example.com', 60000000000, 10000000000, 204);`); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenMigratesV6WithBuiltinDefaultsAndKeepsMonitors(t *testing.T) {
	path := v6Database(t)
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("database schema version 6 is older than this up (%d)", schemaVersion)) {
		t.Fatalf("OpenReadOnly(v6) error = %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v6) error = %v", err)
	}
	defer s.Close()
	if got := version(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	ctx := context.Background()
	if d, err := s.Defaults(ctx); err != nil || d != config.BuiltinDefaults() {
		t.Fatalf("Defaults() = %+v, %v, want built-in", d, err)
	}
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 1 || checks[0].Interval != time.Minute || checks[0].ExpectedStatus != 204 {
		t.Fatalf("Checks() = %+v, %v, want the untouched monitor", checks, err)
	}
}

// v7Database writes a database as the previous release left it (migrations 0
// to 6) with rows in every table that has user data.
func v7Database(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "up.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, m := range migrations[:7] {
		if _, err := db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 7;
INSERT INTO smtp_servers (name, host, port, tls, username, password, password_key, sender) VALUES ('smtp', 'mail.example.com', 587, 'starttls', 'u', x'0102', x'0304', 'up@example.com');
INSERT INTO channels (name, kind, smtp_server, recipients) VALUES ('ops', 'email', 'smtp', 'a@example.com');
INSERT INTO checks (position, name, url, interval_ns, timeout_ns, expected_status, channel) VALUES (0, 'api', 'https://api.example.com', 60000000000, 10000000000, 204, 'ops');
INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error) VALUES ('api', 1000, 'down', 5, 502, 'bad gateway');
INSERT INTO mutes (check_name, until_ns, was_alerting) VALUES ('api', 5000, 1);
INSERT INTO alert_state (check_name, known_alerting) VALUES ('api', 1);
INSERT INTO defaults (id, zone, channel, interval_ns, timeout_ns, expected_status, alert_after, recover_after) VALUES (1, 'UTC', 'ops', 60000000000, 10000000000, 200, 2, 2);
INSERT INTO daemon (id, last_beat_ns) VALUES (1, 42);`); err != nil {
		t.Fatal(err)
	}
	return path
}

// dumpTables returns every row of the named tables as text, in a fixed order.
// results rows show the columns schema v1 created: later schemas only add
// columns to it, with defaults, which no older row was written with.
func dumpTables(t *testing.T, path string, tables ...string) map[string][]string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[string][]string{}
	for _, table := range tables {
		columns := "*"
		if table == "results" {
			columns = "id, check_name, at_ns, status, latency_ns, status_code, error"
		}
		rows, err := db.Query("SELECT " + columns + " FROM " + table + " ORDER BY 1, 2")
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], fmt.Sprintf("%#v", vals))
		}
		rows.Close()
	}
	return out
}

func TestOpenMigratesV7AddingOnlyTheAttemptLog(t *testing.T) {
	path := v7Database(t)
	// checks gains columns in v9 and channels in v10; their earlier columns
	// are compared alone.
	tables := []string{"smtp_servers", "results", "mutes", "alert_state", "defaults", "daemon", "certs", "maintenance_windows", "channel_tests", "secret_key"}
	before, beforeChecks, beforeChannels := dumpTables(t, path, tables...), v8Checks(t, path), v9Channels(t, path)
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("database schema version 7 is older than this up (%d)", schemaVersion)) {
		t.Fatalf("OpenReadOnly(v7) error = %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v7) error = %v", err)
	}
	defer s.Close()
	if got := version(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	if after := dumpTables(t, path, tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v8Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("migration changed existing checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	if after := v9Channels(t, path); !reflect.DeepEqual(beforeChannels, after) {
		t.Fatalf("migration changed existing channels:\nbefore %v\nafter  %v", beforeChannels, after)
	}
	ctx := context.Background()
	if got, err := s.Attempts(ctx, AttemptFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("Attempts() after migration = %+v, %v, want none", got, err)
	}
	if err := s.RecordAttempt(ctx, Attempt{At: t0, Monitor: "api", Channel: "ops", Event: EventDown, Outcome: OutcomeSent}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly after upgrade: %v", err)
	}
	ro.Close()
}

// A v8 migration that fails part way (its last index name is taken) rolls
// back as a whole: the database stays v7 with its rows and no attempt log.
func TestOpenV7FailingMidMigrationChangesNothing(t *testing.T) {
	path := v7Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE INDEX delivery_attempts_by_monitor ON results (at_ns)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	userTables := []string{"smtp_servers", "channels", "checks", "results", "mutes", "alert_state", "defaults", "daemon", "certs", "maintenance_windows", "channel_tests", "secret_key"}
	before := dumpTables(t, path, userTables...)
	if s, err := Open(path); err == nil || !strings.Contains(err.Error(), "migrate schema 7 to 8") {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open(v7 with a clashing index) error = %v, want the 7 to 8 migration to fail", err)
	}
	if got := version(t, path); got != 7 {
		t.Fatalf("user_version = %d, want 7", got)
	}
	for _, name := range tables(t, path) {
		if name == "delivery_attempts" {
			t.Fatal("delivery_attempts exists after a failed migration")
		}
	}
	if after := dumpTables(t, path, userTables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed migration changed rows:\nbefore %v\nafter  %v", before, after)
	}
}
