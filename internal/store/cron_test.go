package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
)

// cronVersion is the schema version that adds heartbeat cron schedules.
const cronVersion = conditionsVersion + 1

var cronCheck = config.Check{Name: "nightly", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "0 2 * * 1-5", Grace: 30 * time.Minute}

func TestCronHeartbeatRoundTripsThroughEveryWrite(t *testing.T) {
	s, key := keyedStore(t)
	ctx := context.Background()
	spaced := cronCheck
	spaced.Schedule, spaced.Cron = "cron", "0  2 * * 1-5"
	if err := s.AddCheck(ctx, spaced); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Checks(ctx); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], cronCheck) {
		t.Fatalf("Checks() = %+v, %v, want %+v", got, err, cronCheck)
	}
	tok := token(t, s, key, "nightly")
	edited := cronCheck
	edited.Cron = "@daily"
	if err := s.UpdateCheck(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if got := token(t, s, key, "nightly"); got != tok {
		t.Fatal("editing the cron replaced the token")
	}
	if err := s.SetChecks(ctx, []config.Check{edited, beatCheck}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Checks(ctx)
	if err != nil || len(got) != 2 || !reflect.DeepEqual(got[0], edited) || !reflect.DeepEqual(got[1], beatCheck) {
		t.Fatalf("Checks() after edits = %+v, %v", got, err)
	}
	if got[1].Schedule != "" || got[1].Cron != "" {
		t.Fatalf("a period heartbeat reads schedule %q cron %q", got[1].Schedule, got[1].Cron)
	}
}

// Edit can switch an existing period heartbeat to cron (and back) and the
// job's ping URL keeps working.
func TestSwitchingAHeartbeatToCronKeepsItsToken(t *testing.T) {
	s, key := keyedStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, beatCheck); err != nil {
		t.Fatal(err)
	}
	tok := token(t, s, key, "backup")
	toCron := beatCheck
	toCron.Period, toCron.Schedule, toCron.Cron = 0, config.ScheduleCron, "0 2 * * *"
	if err := s.UpdateCheck(ctx, toCron); err != nil {
		t.Fatal(err)
	}
	if got := token(t, s, key, "backup"); got != tok {
		t.Fatal("switching to cron replaced the token")
	}
	if !ping(t, s, tok, false, t0) {
		t.Fatal("the token stopped matching after switching to cron")
	}
	if err := s.UpdateCheck(ctx, beatCheck); err != nil {
		t.Fatal(err)
	}
	if got := token(t, s, key, "backup"); got != tok {
		t.Fatal("switching back to period replaced the token")
	}
}

func TestInvalidCronIsRejectedUnchanged(t *testing.T) {
	s, _ := keyedStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, cronCheck); err != nil {
		t.Fatal(err)
	}
	bad := cronCheck
	bad.Cron = "0 25 * * *"
	want := `cron "0 25 * * *" is not valid: hour 25 is out of range 0-23`
	if err := s.UpdateCheck(ctx, bad); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), want) {
		t.Fatalf("UpdateCheck(invalid cron) = %v", err)
	}
	bad.Name = "other"
	if err := s.AddCheck(ctx, bad); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), want) {
		t.Fatalf("AddCheck(invalid cron) = %v", err)
	}
	if got, err := s.Checks(ctx); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], cronCheck) {
		t.Fatalf("Checks() after rejections = %+v, %v", got, err)
	}
}

func TestRecordMissedRefusesAChangedSchedule(t *testing.T) {
	s, key := keyedStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, cronCheck); err != nil {
		t.Fatal(err)
	}
	ping(t, s, token(t, s, key, "nightly"), false, t0)
	r := check.Result{CheckName: "nightly", At: t0.Add(24 * time.Hour), Status: check.StatusDown, Error: "no ping for the 02:00 UTC run"}
	for name, stale := range map[string]config.Check{
		"cron":     {Name: "nightly", Kind: config.CheckHeartbeat, Schedule: config.ScheduleCron, Cron: "0 3 * * 1-5", Grace: 30 * time.Minute},
		"schedule": {Name: "nightly", Kind: config.CheckHeartbeat, Period: 0, Grace: 30 * time.Minute},
	} {
		if stored, err := s.RecordMissed(ctx, stale, t0, r); err != nil || stored {
			t.Fatalf("RecordMissed(stale %s) = %v, %v, want refused", name, stored, err)
		}
	}
	if stored, err := s.RecordMissed(ctx, cronCheck, t0, r); err != nil || !stored {
		t.Fatalf("RecordMissed() = %v, %v, want stored", stored, err)
	}
}

// v11Database writes a database as the previous release left it (migrations
// 0 to 10) with a period heartbeat that has a token and pings.
func v11Database(t *testing.T) string {
	t.Helper()
	path := v10Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(migrations[cronVersion-2]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d;
UPDATE checks SET conditions = '[{"k":"json","p":"status","v":"ok"}]' WHERE name = 'adv';
UPDATE checks SET ping_token = x'01', ping_token_key = x'02', ping_token_hash = x'03' WHERE name = 'job';
INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error) VALUES ('job', 1000, 'up', 0, 0, '');`, cronVersion-1)); err != nil {
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
	rows, err := db.Query("SELECT quote(conditions) FROM checks ORDER BY position")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return append(out, v9Checks(t, path)...)
}

func TestOpenMigratesV11AddingOnlyTheSchedule(t *testing.T) {
	if schemaVersion != cronVersion {
		t.Fatalf("schemaVersion = %d, want %d", schemaVersion, cronVersion)
	}
	path := v11Database(t)
	tables := append([]string{"results"}, v10Tables...)
	before, beforeChecks := dumpTables(t, path, tables...), v11Checks(t, path)
	want := fmt.Sprintf("database schema version %d is older than this up (%d)", cronVersion-1, cronVersion)
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("OpenReadOnly(v11) error = %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v11) error = %v", err)
	}
	defer s.Close()
	if got := version(t, path); got != cronVersion {
		t.Fatalf("user_version = %d, want %d", got, cronVersion)
	}
	if after := dumpTables(t, path, tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v11Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("migration changed existing checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	ctx := context.Background()
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 4 {
		t.Fatalf("Checks() after migration = %+v, %v", checks, err)
	}
	for _, c := range checks {
		if c.Schedule != "" || c.Cron != "" {
			t.Fatalf("%s has a schedule after migration: %q %q", c.Name, c.Schedule, c.Cron)
		}
		if err := config.ValidateCheck(c); err != nil {
			t.Fatalf("%s invalid after migration: %v", c.Name, err)
		}
	}
	job := checks[3]
	if job.Name != "job" || job.Period != time.Hour {
		t.Fatalf("migrated heartbeat = %+v", job)
	}
	// The existing heartbeat stays a period one; switching it to cron keeps
	// its token columns.
	job.Period, job.Schedule, job.Cron = 0, config.ScheduleCron, "30 1 * * *"
	if err := s.UpdateCheck(ctx, job); err != nil {
		t.Fatal(err)
	}
	if after := v9Checks(t, path)[3]; !strings.HasSuffix(after, "|X'01'|X'02'|X'03'") {
		t.Fatalf("switched heartbeat row = %s, want its token kept", after)
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
	if err != nil || !reflect.DeepEqual(got[3], job) {
		t.Fatalf("reopened heartbeat = %+v, %v, want %+v", got, err, job)
	}
}

// A migration that fails (the column already exists) rolls back as a whole.
func TestOpenV11FailingMigrationChangesNothing(t *testing.T) {
	path := v11Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE checks ADD COLUMN cron INTEGER"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, beforeChecks := dumpTables(t, path, v10Tables...), v11Checks(t, path)
	step := fmt.Sprintf("migrate schema %d to %d", cronVersion-1, cronVersion)
	if s, err := Open(path); err == nil || !strings.Contains(err.Error(), step) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open(v11 with a clashing column) error = %v, want %q", err, step)
	}
	if got := version(t, path); got != cronVersion-1 {
		t.Fatalf("user_version = %d, want %d", got, cronVersion-1)
	}
	if after := dumpTables(t, path, v10Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed migration changed rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v11Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("failed migration changed checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
}

func TestNewDatabaseHasScheduleColumns(t *testing.T) {
	s, _ := keyedStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, cronCheck); err != nil {
		t.Fatal(err)
	}
	var schedule, cron string
	if err := s.db.QueryRowContext(ctx, "SELECT schedule, cron FROM checks WHERE name = 'nightly'").Scan(&schedule, &cron); err != nil || schedule != "cron" || cron != "0 2 * * 1-5" {
		t.Fatalf("schedule, cron = %q, %q, %v", schedule, cron, err)
	}
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil || v != cronVersion {
		t.Fatalf("user_version = %d, %v, want %d", v, err, cronVersion)
	}
}
