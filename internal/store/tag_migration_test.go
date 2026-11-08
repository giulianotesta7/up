package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
)

// tagsVersion is the schema version that adds monitor tags.
const tagsVersion = 12

// v11Database writes a database as the previous release left it (migrations
// 0 to 10) with rows in every table that has user data, including a mute and
// a maintenance window.
func v11Database(t *testing.T) string {
	t.Helper()
	path := v10Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(migrations[tagsVersion-2]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d;
UPDATE checks SET conditions = '[{"k":"json","p":"status","v":"ok"}]' WHERE name = 'adv';
INSERT INTO maintenance_windows (check_name, weekdays, start_minute, duration_ns, zone) VALUES ('api', 2, 60, 3600000000000, 'UTC');`, tagsVersion-1)); err != nil {
		t.Fatal(err)
	}
	return path
}

// v11Checks dumps the checks columns that schema v11 has.
func v11Checks(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var out []string
	rows, err := db.Query("SELECT quote(name) || '|' || quote(conditions) FROM checks ORDER BY position")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return append(out, v9Checks(t, path)...)
}

// v11Windows dumps the maintenance window columns that schema v11 has.
func v11Windows(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT id || '|' || check_name || '|' || weekdays || '|' || start_minute || '|' || duration_ns || '|' || zone FROM maintenance_windows ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// v11Tables are the tables whose rows and columns v12 leaves alone.
var v11Tables = []string{"channels", "smtp_servers", "results", "mutes", "alert_state", "defaults", "daemon", "daemon_api", "certs", "channel_tests", "secret_key", "delivery_attempts"}

func TestOpenMigratesV11AddingOnlyTags(t *testing.T) {
	if schemaVersion != tagsVersion {
		t.Fatalf("schemaVersion = %d, want %d", schemaVersion, tagsVersion)
	}
	path := v11Database(t)
	before, beforeChecks, beforeWindows := dumpTables(t, path, v11Tables...), v11Checks(t, path), v11Windows(t, path)
	if len(beforeWindows) == 0 || len(before["mutes"]) == 0 {
		t.Fatalf("the v11 fixture needs a window and a mute: %v %v", beforeWindows, before["mutes"])
	}
	want := fmt.Sprintf("database schema version %d is older than this up (%d)", tagsVersion-1, tagsVersion)
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("OpenReadOnly(v11) error = %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v11) error = %v", err)
	}
	defer s.Close()
	if got := version(t, path); got != tagsVersion {
		t.Fatalf("user_version = %d, want %d", got, tagsVersion)
	}
	if after := dumpTables(t, path, v11Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v11Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("migration changed existing checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	if after := v11Windows(t, path); !reflect.DeepEqual(beforeWindows, after) {
		t.Fatalf("migration changed existing windows:\nbefore %v\nafter  %v", beforeWindows, after)
	}
	ctx := context.Background()
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 4 {
		t.Fatalf("Checks() after migration = %+v, %v", checks, err)
	}
	for _, c := range checks {
		if c.Tags != nil {
			t.Fatalf("%s has tags after migration: %q", c.Name, c.Tags)
		}
	}
	windows, err := s.AllMaintenanceWindows(ctx)
	if err != nil || len(windows) != len(beforeWindows) || windows[0].Tag != "" {
		t.Fatalf("windows after migration = %+v, %v", windows, err)
	}
	// A migrated monitor gains tags on edit, and its tag can be muted.
	edited := checks[0]
	edited.Tags = []string{"nas"}
	if err := s.UpdateCheck(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTagMute(ctx, "nas", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
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
	if err != nil || !reflect.DeepEqual(got[0].Tags, []string{"nas"}) {
		t.Fatalf("reopened tags = %+v, %v", got, err)
	}
	if mutes, err := ro.TagMutes(ctx); err != nil || len(mutes) != 1 || mutes[0].Tag != "nas" {
		t.Fatalf("reopened tag mutes = %+v, %v", mutes, err)
	}
}

// A migration that fails (the column already exists) rolls back as a whole:
// the database stays v11 with its rows.
func TestOpenV11FailingMigrationChangesNothing(t *testing.T) {
	path := v11Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE maintenance_windows ADD COLUMN tag INTEGER"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, beforeChecks := dumpTables(t, path, v11Tables...), v11Checks(t, path)
	step := fmt.Sprintf("migrate schema %d to %d", tagsVersion-1, tagsVersion)
	if s, err := Open(path); err == nil || !strings.Contains(err.Error(), step) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open(v11 with a clashing column) error = %v, want %q", err, step)
	}
	if got := version(t, path); got != tagsVersion-1 {
		t.Fatalf("user_version = %d, want %d", got, tagsVersion-1)
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
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name = 'tag_mutes'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("tag_mutes exists after a failed migration: %d, %v", n, err)
	}
}

// A database written by a newer up is refused, and a new database starts at
// this version with empty tags.
func TestNewDatabaseHasTags(t *testing.T) {
	path := t.TempDir() + "/up.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddCheck(context.Background(), config.Check{Name: "api", URL: "https://example.com", Interval: 60e9, Timeout: 5e9, ExpectedStatus: 200}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := version(t, path); got != tagsVersion {
		t.Fatalf("user_version = %d, want %d", got, tagsVersion)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	if err := db.QueryRow("SELECT tags FROM checks WHERE name = 'api'").Scan(&text); err != nil || text != "" {
		t.Fatalf("tags column = %q, %v, want empty", text, err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", tagsVersion+1)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	want := fmt.Sprintf("database schema version %d is newer than this up supports (%d)", tagsVersion+1, tagsVersion)
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Open(newer) error = %v", err)
	}
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("OpenReadOnly(newer) error = %v", err)
	}
}
