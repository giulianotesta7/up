package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

// conditionsVersion is the schema version that adds HTTP response conditions.
const conditionsVersion = 10

// v9Database writes a database as the previous release left it (migrations 0
// to 8) with rows in every table that has user data, including an advanced
// HTTP check and a heartbeat.
func v9Database(t *testing.T) string {
	t.Helper()
	path := v8Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(migrations[conditionsVersion-2]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d;
INSERT INTO checks (position, name, url, interval_ns, timeout_ns, expected_status, method, headers, expect_text) VALUES (2, 'adv', 'https://example.com', 60000000000, 5000000000, 200, 'POST', '[{"n":"Accept"}]', 'pong');
INSERT INTO checks (position, name, url, interval_ns, timeout_ns, expected_status, kind, period_ns, grace_ns) VALUES (3, 'job', '', 0, 0, 0, 'heartbeat', 3600000000000, 60000000000);
INSERT INTO daemon_api (id, listen, tls) VALUES (1, ':8080', 0);`, conditionsVersion-1)); err != nil {
		t.Fatal(err)
	}
	return path
}

var v9Tables = append([]string{"daemon_api"}, v8Tables...)

// v9Checks dumps the checks columns that schema v9 has.
func v9Checks(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cols := strings.Split("position, name, kind, url, interval_ns, timeout_ns, expected_status, channel, alert_after, recover_after, tls_off, method, headers, body, body_key, expect_text, host, port, period_ns, grace_ns, ping_token, ping_token_key, ping_token_hash", ", ")
	rows, err := db.Query("SELECT quote(" + strings.Join(cols, "), quote(") + ") FROM checks ORDER BY position")
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

func TestOpenMigratesV9AddingOnlyConditions(t *testing.T) {
	if schemaVersion != conditionsVersion {
		t.Fatalf("schemaVersion = %d, want %d", schemaVersion, conditionsVersion)
	}
	path := v9Database(t)
	before, beforeChecks := dumpTables(t, path, v9Tables...), v9Checks(t, path)
	want := fmt.Sprintf("database schema version %d is older than this up (%d)", conditionsVersion-1, conditionsVersion)
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("OpenReadOnly(v9) error = %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v9) error = %v", err)
	}
	defer s.Close()
	if got := version(t, path); got != conditionsVersion {
		t.Fatalf("user_version = %d, want %d", got, conditionsVersion)
	}
	if after := dumpTables(t, path, v9Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v9Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("migration changed existing checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	ctx := context.Background()
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 4 {
		t.Fatalf("Checks() after migration = %+v, %v", checks, err)
	}
	for _, c := range checks {
		if c.Conditions != nil {
			t.Fatalf("%s has conditions after migration: %+v", c.Name, c.Conditions)
		}
		if err := config.ValidateCheck(c); err != nil {
			t.Fatalf("%s invalid after migration: %v", c.Name, err)
		}
	}
	// Migrated monitors keep accepting results for their unchanged definition.
	if stored, err := s.RecordDefined(ctx, checks[2], check.Result{CheckName: "adv", At: t0, Status: check.StatusUp}); err != nil || !stored {
		t.Fatalf("RecordDefined() after migration = %v, %v", stored, err)
	}
	// A migrated monitor gains conditions on edit.
	edited := checks[2]
	edited.Conditions = []config.Condition{{Kind: config.ConditionJSON, Path: "status", Value: "ok"}}
	if err := s.UpdateCheck(ctx, edited); err != nil {
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
	if err != nil || !reflect.DeepEqual(got[2].Conditions, edited.Conditions) {
		t.Fatalf("reopened conditions = %+v, %v", got, err)
	}
}

// A migration that fails (the column already exists) rolls back as a whole:
// the database stays v9 with its rows.
func TestOpenV9FailingMigrationChangesNothing(t *testing.T) {
	path := v9Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE checks ADD COLUMN conditions INTEGER"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, beforeChecks := dumpTables(t, path, v9Tables...), v9Checks(t, path)
	step := fmt.Sprintf("migrate schema %d to %d", conditionsVersion-1, conditionsVersion)
	if s, err := Open(path); err == nil || !strings.Contains(err.Error(), step) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open(v9 with a clashing column) error = %v, want %q", err, step)
	}
	if got := version(t, path); got != conditionsVersion-1 {
		t.Fatalf("user_version = %d, want %d", got, conditionsVersion-1)
	}
	if after := dumpTables(t, path, v9Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed migration changed rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v9Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("failed migration changed checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
}

// A database written by a newer up is refused, and a new database starts at
// this version with an empty conditions column.
func TestNewDatabaseHasConditionsColumn(t *testing.T) {
	path := t.TempDir() + "/up.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddCheck(context.Background(), config.Check{Name: "api", URL: "https://example.com", Interval: 60e9, Timeout: 5e9, ExpectedStatus: 200}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := version(t, path); got != conditionsVersion {
		t.Fatalf("user_version = %d, want %d", got, conditionsVersion)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var text string
	if err := db.QueryRow("SELECT conditions FROM checks WHERE name = 'api'").Scan(&text); err != nil || text != "" {
		t.Fatalf("conditions column = %q, %v, want empty", text, err)
	}
}
