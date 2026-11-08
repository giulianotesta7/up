package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
)

// probesVersion is the schema version that adds ping and DNS monitors.
const probesVersion = 12

// v11Database writes a database as the previous release left it (migrations
// 0 to 10) with another TCP check, a check with conditions and a result.
func v11Database(t *testing.T) string {
	t.Helper()
	path := v10Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(migrations[probesVersion-2]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d;
INSERT INTO checks (position, name, url, interval_ns, timeout_ns, expected_status, kind, host, port) VALUES (4, 'cache', '', 30000000000, 5000000000, 0, 'tcp', 'cache.example.com', 6379);
INSERT INTO checks (position, name, url, interval_ns, timeout_ns, expected_status, conditions) VALUES (5, 'json', 'https://example.com/health', 60000000000, 5000000000, 200, '[{"k":"json","p":"status","v":"ok"}]');
INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error) VALUES ('cache', 1, 'down', 0, 0, 'connection refused');`, probesVersion-1)); err != nil {
		t.Fatal(err)
	}
	return path
}

// v11Results dumps the results columns that schemas before v12 have.
func v11Results(t *testing.T, path string) []string {
	t.Helper()
	return dumpColumns(t, path, "results", "id, check_name, at_ns, status, latency_ns, status_code, error")
}

// v11Checks dumps the checks columns that schema v11 has.
func v11Checks(t *testing.T, path string) []string {
	t.Helper()
	return dumpColumns(t, path, "checks", "position, name, kind, url, interval_ns, timeout_ns, expected_status, channel, alert_after, recover_after, tls_off, method, headers, body, body_key, expect_text, host, port, period_ns, grace_ns, ping_token, ping_token_key, ping_token_hash, conditions")
}

func dumpColumns(t *testing.T, path, table, columns string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cols := strings.Split(columns, ", ")
	rows, err := db.Query("SELECT quote(" + strings.Join(cols, "), quote(") + ") FROM " + table + " ORDER BY 1, 2")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals := make([]string, len(cols))
		ptrs := make([]any, len(vals))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, strings.Join(vals, "|"))
	}
	return out
}

// v11Tables are the tables whose rows and columns v12 leaves alone.
var v11Tables = []string{"smtp_servers", "channels", "mutes", "alert_state", "defaults", "daemon", "daemon_api", "certs", "maintenance_windows", "channel_tests", "secret_key", "delivery_attempts"}

func TestOpenMigratesV11AddingOnlyPingAndDNS(t *testing.T) {
	if schemaVersion < probesVersion {
		t.Fatalf("schemaVersion = %d, want at least %d", schemaVersion, probesVersion)
	}
	path := v11Database(t)
	before, beforeChecks, beforeResults := dumpTables(t, path, v11Tables...), v11Checks(t, path), v11Results(t, path)
	want := fmt.Sprintf("database schema version %d is older than this up (%d)", probesVersion-1, schemaVersion)
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("OpenReadOnly(v11) error = %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v11) error = %v", err)
	}
	defer s.Close()
	if got := version(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	if after := dumpTables(t, path, v11Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v11Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("migration changed existing checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	if after := v11Results(t, path); !reflect.DeepEqual(beforeResults, after) {
		t.Fatalf("migration changed existing results:\nbefore %v\nafter  %v", beforeResults, after)
	}
	ctx := context.Background()
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 6 {
		t.Fatalf("Checks() after migration = %+v, %v", checks, err)
	}
	for _, c := range checks {
		if c.Domain != "" || c.Record != "" || c.Resolver != "" || c.Expected != "" {
			t.Fatalf("%s has DNS fields after migration: %+v", c.Name, c)
		}
		if err := config.ValidateCheck(c); err != nil {
			t.Fatalf("%s invalid after migration: %v", c.Name, err)
		}
	}
	if r, ok, err := s.Latest(ctx, "cache"); err != nil || !ok || r.Answer != "" || r.Answers != 0 || r.Error != "connection refused" {
		t.Fatalf("Latest(cache) after migration = %+v, %v, %v", r, ok, err)
	}
	if p, err := s.PingPermission(ctx); err != nil || p != config.PingUnknown {
		t.Fatalf("PingPermission() after migration = %q, %v, want unknown", p, err)
	}
	// Migrated monitors keep accepting results for their unchanged definition.
	if stored, err := s.RecordDefined(ctx, checks[4], check.Result{CheckName: "cache", At: t0, Status: check.StatusUp}); err != nil || !stored {
		t.Fatalf("RecordDefined(cache) after migration = %v, %v", stored, err)
	}
	// New kinds are stored and survive a read-only reopen.
	dnsCheck := config.Check{Name: "nas", Kind: config.CheckDNS, Domain: "nas.home.arpa", Record: "TXT", Expected: "v=1", Interval: time.Minute, Timeout: time.Second}
	pingCheck := config.Check{Name: "router", Kind: config.CheckPing, Host: "192.168.1.1", Interval: time.Minute, Timeout: time.Second}
	for _, c := range []config.Check{dnsCheck, pingCheck} {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	answered := check.Result{CheckName: "nas", At: t0, Status: check.StatusUp, Answer: "v=1", Answers: 1}
	if err := s.Record(ctx, answered); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPingPermission(ctx, config.PingDenied); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly after upgrade: %v", err)
	}
	defer ro.Close()
	got, err := ro.Checks(ctx)
	if err != nil || len(got) != 8 || !got[6].SameDefinition(dnsCheck) || !got[7].SameDefinition(pingCheck) {
		t.Fatalf("reopened checks = %+v, %v", got, err)
	}
	if r, ok, err := ro.Latest(ctx, "nas"); err != nil || !ok || r != answered {
		t.Fatalf("reopened Latest(nas) = %+v, %v, %v", r, ok, err)
	}
	if p, err := ro.PingPermission(ctx); err != nil || p != config.PingDenied {
		t.Fatalf("reopened PingPermission() = %q, %v", p, err)
	}
}

// A migration that fails (a column already exists) rolls back as a whole:
// the database stays v11 with its rows.
func TestOpenV11FailingMigrationChangesNothing(t *testing.T) {
	path := v11Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE results ADD COLUMN answers INTEGER"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, beforeChecks := dumpTables(t, path, v11Tables...), v11Checks(t, path)
	step := fmt.Sprintf("migrate schema %d to %d", probesVersion-1, probesVersion)
	if s, err := Open(path); err == nil || !strings.Contains(err.Error(), step) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open(v11 with a clashing column) error = %v, want %q", err, step)
	}
	if got := version(t, path); got != probesVersion-1 {
		t.Fatalf("user_version = %d, want %d", got, probesVersion-1)
	}
	if after := dumpTables(t, path, v11Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed migration changed rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v11Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("failed migration changed checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	db, err = sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('checks') WHERE name IN ('domain', 'record', 'resolver', 'expected')").Scan(&n); err != nil || n != 0 {
		t.Fatalf("DNS columns after a failed migration = %d, %v", n, err)
	}
}

// A new database starts at this version with empty DNS columns.
func TestNewDatabaseHasPingAndDNSColumns(t *testing.T) {
	path := t.TempDir() + "/up.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddCheck(context.Background(), config.Check{Name: "api", URL: "https://example.com", Interval: 60e9, Timeout: 5e9, ExpectedStatus: 200}); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(context.Background(), check.Result{CheckName: "api", At: t0, Status: check.StatusUp}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := version(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	if got := dumpColumns(t, path, "checks", "domain, record, resolver, expected"); !reflect.DeepEqual(got, []string{"''|''|''|''"}) {
		t.Fatalf("DNS columns = %q, want empty", got)
	}
	if got := dumpColumns(t, path, "results", "answer, answers"); !reflect.DeepEqual(got, []string{"''|0"}) {
		t.Fatalf("answer columns = %q, want empty", got)
	}
}
