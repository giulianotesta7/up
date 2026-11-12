package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
)

// cronVersion is the schema version that adds heartbeat cron schedules.
const cronVersion = probesVersion + 1

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
	r := check.Result{CheckName: "nightly", At: t0.Add(24 * time.Hour), Status: check.StatusDown, Error: "no ping for the 02:00 UTC run", Run: t0.Add(23*time.Hour + 30*time.Minute)}
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
	if got, err := s.Recent(ctx, "nightly", 2); err != nil || len(got) != 2 || !got[0].Run.Equal(r.Run) || !got[1].Run.IsZero() {
		t.Fatalf("Recent() = %+v, %v, want the miss's run and none for the ping", got, err)
	}
}

// v13Database writes a database as the previous release left it (migrations
// 0 to 12, built from the v12 fixture through the real ping/DNS step) with a
// period heartbeat that has a token and pings, a DNS check and an answered
// result.
func v13Database(t *testing.T) string {
	t.Helper()
	path := v12Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(migrations[cronVersion-2]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d;
UPDATE checks SET ping_token = x'01', ping_token_key = x'02', ping_token_hash = x'03' WHERE name = 'job';
INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error) VALUES ('job', 1000, 'up', 0, 0, '');
INSERT INTO checks (position, name, url, interval_ns, timeout_ns, expected_status, kind, domain, record, resolver, expected, tags) VALUES (6, 'nas', '', 60000000000, 5000000000, 0, 'dns', 'nas.home.arpa', 'TXT', '192.168.1.1:53', 'v=1', 'home');
INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error, answer, answers) VALUES ('nas', 2000, 'up', 0, 0, '', 'v=1', 1);
INSERT INTO daemon_pings (id, permission) VALUES (1, 'denied');`, cronVersion-1)); err != nil {
		t.Fatal(err)
	}
	return path
}

// v13Checks dumps the checks columns that schema v13 has.
func v13Checks(t *testing.T, path string) []string {
	t.Helper()
	return dumpColumns(t, path, "checks", "position, name, kind, url, interval_ns, timeout_ns, expected_status, channel, alert_after, recover_after, tls_off, method, headers, body, body_key, expect_text, host, port, period_ns, grace_ns, ping_token, ping_token_key, ping_token_hash, conditions, tags, domain, record, resolver, expected")
}

// v13Results dumps the results columns that schema v13 has.
func v13Results(t *testing.T, path string) []string {
	t.Helper()
	return dumpColumns(t, path, "results", "id, check_name, at_ns, status, latency_ns, status_code, error, answer, answers")
}

// v13Tables are the tables whose rows and columns v14 leaves alone.
var v13Tables = append([]string{"daemon_pings"}, v12Tables...)

func TestOpenMigratesV13AddingOnlyTheSchedule(t *testing.T) {
	if schemaVersion < cronVersion {
		t.Fatalf("schemaVersion = %d, want at least %d", schemaVersion, cronVersion)
	}
	path := v13Database(t)
	before, beforeChecks, beforeResults := dumpTables(t, path, v13Tables...), v13Checks(t, path), v13Results(t, path)
	want := fmt.Sprintf("database schema version %d is older than this up (%d)", cronVersion-1, schemaVersion)
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("OpenReadOnly(v13) error = %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v13) error = %v", err)
	}
	defer s.Close()
	if got := version(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	if after := dumpTables(t, path, v13Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v13Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("migration changed existing checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	if after := v13Results(t, path); !reflect.DeepEqual(beforeResults, after) {
		t.Fatalf("migration changed existing results:\nbefore %v\nafter  %v", beforeResults, after)
	}
	ctx := context.Background()
	var runs string
	if err := s.db.QueryRowContext(ctx, "SELECT group_concat(DISTINCT run_ns) FROM results").Scan(&runs); err != nil || runs != "0" {
		t.Fatalf("results run_ns after migration = %q, %v, want 0", runs, err)
	}
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 7 {
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
	if nas := checks[6]; nas.Name != "nas" || nas.Domain != "nas.home.arpa" || nas.Expected != "v=1" || !reflect.DeepEqual(nas.Tags, []string{"home"}) {
		t.Fatalf("migrated DNS check = %+v", nas)
	}
	if r, ok, err := s.Latest(ctx, "nas"); err != nil || !ok || r.Answer != "v=1" || r.Answers != 1 || !r.Run.IsZero() {
		t.Fatalf("Latest(nas) after migration = %+v, %v, %v", r, ok, err)
	}
	if p, err := s.PingPermission(ctx); err != nil || p != config.PingDenied {
		t.Fatalf("PingPermission() after migration = %q, %v", p, err)
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

// A migration that fails (the column already exists) rolls back as a whole:
// the database stays v13 with its rows.
func TestOpenV13FailingMigrationChangesNothing(t *testing.T) {
	path := v13Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE checks ADD COLUMN cron INTEGER"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, beforeChecks, beforeResults := dumpTables(t, path, v13Tables...), v13Checks(t, path), v13Results(t, path)
	step := fmt.Sprintf("migrate schema %d to %d", cronVersion-1, cronVersion)
	if s, err := Open(path); err == nil || !strings.Contains(err.Error(), step) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open(v13 with a clashing column) error = %v, want %q", err, step)
	}
	if got := version(t, path); got != cronVersion-1 {
		t.Fatalf("user_version = %d, want %d", got, cronVersion-1)
	}
	if after := dumpTables(t, path, v13Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed migration changed rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v13Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("failed migration changed checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	if after := v13Results(t, path); !reflect.DeepEqual(beforeResults, after) {
		t.Fatalf("failed migration changed results:\nbefore %v\nafter  %v", beforeResults, after)
	}
	db, err = sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('results') WHERE name = 'run_ns'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("run_ns column after a failed migration = %d, %v", n, err)
	}
}

// A v11 database migrates through tags (v12), ping/DNS (v13) and cron (v14)
// in one open, keeping its rows and its period heartbeat with its token and
// pings; afterwards tags, ping/DNS fields and cron schedules all persist.
func TestOpenMigratesV11ThroughTagsPingDNSAndCron(t *testing.T) {
	path := v11Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
UPDATE checks SET ping_token = x'01', ping_token_key = x'02', ping_token_hash = x'03' WHERE name = 'job';
INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error) VALUES ('job', 1000, 'up', 0, 0, '');`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, beforeChecks, beforeWindows := dumpTables(t, path, v11Tables...), v11Checks(t, path), v11Windows(t, path)
	if len(beforeWindows) == 0 || len(before["mutes"]) == 0 || len(before["results"]) == 0 {
		t.Fatalf("the v11 fixture needs a window, a mute and a result: %v %v %v", beforeWindows, before["mutes"], before["results"])
	}
	want := fmt.Sprintf("database schema version %d is older than this up (%d)", tagsVersion-1, schemaVersion)
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
	if after := v11Windows(t, path); !reflect.DeepEqual(beforeWindows, after) {
		t.Fatalf("migration changed existing windows:\nbefore %v\nafter  %v", beforeWindows, after)
	}
	ctx := context.Background()
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 4 {
		t.Fatalf("Checks() after migration = %+v, %v", checks, err)
	}
	for _, c := range checks {
		if c.Tags != nil || c.Domain != "" || c.Record != "" || c.Resolver != "" || c.Expected != "" || c.Schedule != "" || c.Cron != "" {
			t.Fatalf("%s has tags, DNS fields or a schedule after migration: %+v", c.Name, c)
		}
		if err := config.ValidateCheck(c); err != nil {
			t.Fatalf("%s invalid after migration: %v", c.Name, err)
		}
	}
	job := checks[3]
	if job.Name != "job" || job.Period != time.Hour {
		t.Fatalf("migrated heartbeat = %+v", job)
	}
	if got := recent(t, s, "job"); len(got) != 1 || got[0].Status != check.StatusUp || !got[0].Run.IsZero() || got[0].Answer != "" {
		t.Fatalf("migrated heartbeat results = %+v", got)
	}
	// Every feature writes on the migrated database: tags on the period
	// heartbeat (token kept), a DNS check with an answer, a ping check and a
	// cron heartbeat with a missed run.
	key, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "up.db.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicKey(ctx, key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	job.Tags = []string{"nas"}
	if err := s.UpdateCheck(ctx, job); err != nil {
		t.Fatal(err)
	}
	if after := v9Checks(t, path)[3]; !strings.HasSuffix(after, "|X'01'|X'02'|X'03'") {
		t.Fatalf("tagged heartbeat row = %s, want its token kept", after)
	}
	dnsCheck := config.Check{Name: "nas", Kind: config.CheckDNS, Domain: "nas.home.arpa", Record: "TXT", Expected: "v=1", Interval: time.Minute, Timeout: time.Second, Tags: []string{"nas"}}
	pingCheck := config.Check{Name: "router", Kind: config.CheckPing, Host: "192.168.1.1", Interval: time.Minute, Timeout: time.Second}
	for _, c := range []config.Check{dnsCheck, pingCheck, cronCheck} {
		if err := s.AddCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	answered := check.Result{CheckName: "nas", At: t0, Status: check.StatusUp, Answer: "v=1", Answers: 1}
	if err := s.Record(ctx, answered); err != nil {
		t.Fatal(err)
	}
	missed := check.Result{CheckName: "nightly", At: t0.Add(time.Hour), Status: check.StatusDown, Error: "no ping for the 02:00 UTC run", Run: t0}
	if stored, err := s.RecordMissed(ctx, cronCheck, time.Time{}, missed); err != nil || !stored {
		t.Fatalf("RecordMissed() = %v, %v, want stored", stored, err)
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
	if err != nil || len(got) != 7 || !reflect.DeepEqual(got[3], job) || !got[4].SameDefinition(dnsCheck) || !reflect.DeepEqual(got[4].Tags, []string{"nas"}) ||
		!got[5].SameDefinition(pingCheck) || !reflect.DeepEqual(got[6], cronCheck) {
		t.Fatalf("reopened checks = %+v, %v", got, err)
	}
	if r, ok, err := ro.Latest(ctx, "nas"); err != nil || !ok || r != answered {
		t.Fatalf("reopened Latest(nas) = %+v, %v, %v", r, ok, err)
	}
	if r, err := ro.Recent(ctx, "nightly", 1); err != nil || len(r) != 1 || !r[0].Run.Equal(t0) || r[0].Error != missed.Error {
		t.Fatalf("reopened Recent(nightly) = %+v, %v", r, err)
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
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil || v != schemaVersion {
		t.Fatalf("user_version = %d, %v, want %d", v, err, schemaVersion)
	}
}
