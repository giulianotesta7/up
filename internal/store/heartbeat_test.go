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

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

var beatCheck = config.Check{Name: "backup", Kind: config.CheckHeartbeat, Period: time.Hour, Grace: 10 * time.Minute}

// keyedStore is a temporary store with the daemon's public key set, as
// `up serve` leaves it, and that key.
func keyedStore(t *testing.T) (*Store, *secret.Key) {
	t.Helper()
	s, _ := openTemp(t)
	key, _, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "up.db.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPublicKey(context.Background(), key.PublicKey()); err != nil {
		t.Fatal(err)
	}
	return s, key
}

// token reads and opens the stored token of name.
func token(t *testing.T, s *Store, key *secret.Key, name string) string {
	t.Helper()
	sealed, err := s.HeartbeatToken(context.Background(), name)
	if err != nil {
		t.Fatalf("HeartbeatToken(%s) error = %v", name, err)
	}
	plain, err := key.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	return plain
}

func ping(t *testing.T, s *Store, tok string, failed bool, at time.Time) bool {
	t.Helper()
	found, err := s.PingHeartbeat(context.Background(), tok, failed, at)
	if err != nil {
		t.Fatalf("PingHeartbeat() error = %v", err)
	}
	return found
}

func recent(t *testing.T, s *Store, name string) []check.Result {
	t.Helper()
	got, err := s.Recent(context.Background(), name, 100)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestHeartbeatNeedsTheEncryptionKey(t *testing.T) {
	s, _ := openTemp(t)
	err := s.AddCheck(context.Background(), beatCheck)
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "heartbeat monitors need the encryption key: start `up serve` once") {
		t.Fatalf("AddCheck(heartbeat) without key = %v", err)
	}
	if checks, _ := s.Checks(context.Background()); len(checks) != 0 {
		t.Fatalf("Checks() = %+v, want nothing stored", checks)
	}
}

func TestHeartbeatRoundTripTokenAndPings(t *testing.T) {
	s, key := keyedStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, beatCheck); err != nil {
		t.Fatal(err)
	}
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 1 || !reflect.DeepEqual(checks[0], beatCheck) {
		t.Fatalf("Checks() = %+v, %v, want %+v", checks, err, beatCheck)
	}
	tok := token(t, s, key, "backup")
	if !strings.HasPrefix(tok, "uphb_") || len(tok) != len("uphb_")+43 {
		t.Fatalf("token %q, want uphb_ and 256 random bits in base64url", tok)
	}
	if strings.Contains(fmt.Sprintf("%#v", checks), tok) {
		t.Fatal("Checks() carries the token")
	}

	if ping(t, s, "uphb_"+strings.Repeat("A", 43), false, t0) || ping(t, s, "", false, t0) || ping(t, s, "garbage/../x", true, t0) {
		t.Fatal("an unknown token was accepted")
	}
	if got := recent(t, s, "backup"); len(got) != 0 {
		t.Fatalf("unknown tokens recorded %+v", got)
	}
	if !ping(t, s, tok, false, t0) || !ping(t, s, tok, true, t0.Add(time.Minute)) {
		t.Fatal("the token was refused")
	}
	want := []check.Result{check.Fail("backup", t0.Add(time.Minute)), check.Ping("backup", t0)}
	if got := recent(t, s, "backup"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Recent() = %+v, want %+v", got, want)
	}
}

func TestHeartbeatTokensAreUniqueAndCopiesGetTheirOwn(t *testing.T) {
	s, key := keyedStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, beatCheck); err != nil {
		t.Fatal(err)
	}
	copied := beatCheck
	copied.Name = "backup-copy"
	if err := s.AddCheckFrom(ctx, "backup", copied); err != nil {
		t.Fatal(err)
	}
	if a, b := token(t, s, key, "backup"), token(t, s, key, "backup-copy"); a == b {
		t.Fatal("a copy shares the token of its source")
	}
}

func TestRegenerateHeartbeatToken(t *testing.T) {
	s, key := keyedStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, beatCheck); err != nil {
		t.Fatal(err)
	}
	old := token(t, s, key, "backup")
	fresh, err := s.RegenerateHeartbeatToken(ctx, "backup")
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old || !strings.HasPrefix(fresh, "uphb_") || token(t, s, key, "backup") != fresh {
		t.Fatalf("regenerated %q (old %q), stored %q", fresh, old, token(t, s, key, "backup"))
	}
	if ping(t, s, old, false, t0) {
		t.Fatal("the old token still records pings")
	}
	if !ping(t, s, fresh, false, t0) {
		t.Fatal("the new token is refused")
	}
	if got := recent(t, s, "backup"); len(got) != 1 {
		t.Fatalf("Recent() = %+v, want only the new token's ping", got)
	}

	if err := s.AddCheck(ctx, config.Check{Name: "api", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "api"} {
		if _, err := s.RegenerateHeartbeatToken(ctx, name); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), fmt.Sprintf("heartbeat monitor %q not found", name)) {
			t.Fatalf("RegenerateHeartbeatToken(%s) = %v, want not found", name, err)
		}
		if _, err := s.HeartbeatToken(ctx, name); !errors.Is(err, ErrNotFound) {
			t.Fatalf("HeartbeatToken(%s) = %v, want not found", name, err)
		}
	}
}

func TestHeartbeatKindEditsAndDeleteManageTheToken(t *testing.T) {
	s, key := keyedStore(t)
	ctx := context.Background()
	httpCheck := config.Check{Name: "job", URL: "https://example.com", Interval: time.Minute, Timeout: time.Second, ExpectedStatus: 200}
	if err := s.AddCheck(ctx, httpCheck); err != nil {
		t.Fatal(err)
	}
	asBeat := beatCheck
	asBeat.Name = "job"
	if err := s.UpdateCheck(ctx, asBeat); err != nil {
		t.Fatal(err)
	}
	tok := token(t, s, key, "job")
	// An edit that keeps the heartbeat keeps its token.
	asBeat.Grace = time.Hour
	if err := s.UpdateCheck(ctx, asBeat); err != nil {
		t.Fatal(err)
	}
	if token(t, s, key, "job") != tok || !ping(t, s, tok, false, t0) {
		t.Fatal("editing a heartbeat changed its token")
	}
	if err := s.UpdateCheck(ctx, httpCheck); err != nil {
		t.Fatal(err)
	}
	if ping(t, s, tok, false, t0.Add(time.Minute)) {
		t.Fatal("a check that is no longer a heartbeat accepts pings")
	}
	if err := s.UpdateCheck(ctx, asBeat); err != nil {
		t.Fatal(err)
	}
	tok = token(t, s, key, "job")
	if err := s.DeleteCheck(ctx, "job"); err != nil {
		t.Fatal(err)
	}
	if ping(t, s, tok, false, t0.Add(2*time.Minute)) {
		t.Fatal("a deleted heartbeat accepts pings")
	}
	if got := recent(t, s, "job"); len(got) != 0 {
		t.Fatalf("Recent() = %+v after delete", got)
	}
}

func TestRecordMissedOnlyWithoutANewerResult(t *testing.T) {
	s, key := keyedStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, beatCheck); err != nil {
		t.Fatal(err)
	}
	tok := token(t, s, key, "backup")
	ping(t, s, tok, false, t0)
	missed := check.Overdue(beatCheck, t0, t0, t0.Add(3*time.Hour), time.UTC)
	if len(missed) != 2 {
		t.Fatalf("Overdue() = %+v", missed)
	}
	// A ping that arrived after the loop read t0 wins over the deadline.
	ping(t, s, tok, false, t0.Add(time.Minute))
	if stored, err := s.RecordMissed(ctx, beatCheck, t0, missed[0]); err != nil || stored {
		t.Fatalf("RecordMissed() after a newer ping = %v, %v, want refused", stored, err)
	}
	newest := t0.Add(time.Minute)
	missed = check.Overdue(beatCheck, newest, newest, newest.Add(3*time.Hour), time.UTC)
	if stored, err := s.RecordMissed(ctx, beatCheck, newest, missed[0]); err != nil || !stored {
		t.Fatalf("RecordMissed() = %v, %v, want stored", stored, err)
	}
	if stored, err := s.RecordMissed(ctx, beatCheck, missed[0].At, missed[1]); err != nil || !stored {
		t.Fatalf("RecordMissed(second) = %v, %v, want stored", stored, err)
	}
	// An edited definition refuses results computed for the old one.
	edited := beatCheck
	edited.Period = 2 * time.Hour
	if stored, err := s.RecordMissed(ctx, edited, missed[1].At, check.Result{CheckName: "backup", At: missed[1].At.Add(time.Hour), Status: check.StatusDown, Error: "no ping"}); err != nil || stored {
		t.Fatalf("RecordMissed(stale definition) = %v, %v, want refused", stored, err)
	}
	want := []check.Result{missed[1], missed[0], check.Ping("backup", newest), check.Ping("backup", t0)}
	if got := recent(t, s, "backup"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Recent() = %+v, want %+v", got, want)
	}
}

func TestAPIListenerRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if got, err := s.APIListener(ctx); err != nil || got != (config.APIListener{}) {
		t.Fatalf("APIListener() before serve = %+v, %v, want none", got, err)
	}
	want := config.APIListener{Address: "[::]:8080", TLS: true}
	if err := s.SetAPIListener(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.APIListener(ctx); err != nil || got != want {
		t.Fatalf("APIListener() = %+v, %v, want %+v", got, err, want)
	}
	if err := s.SetAPIListener(ctx, config.APIListener{}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.APIListener(ctx); err != nil || got != (config.APIListener{}) {
		t.Fatalf("APIListener() after a start without API = %+v, %v", got, err)
	}
}

// v8Database writes a database as the previous release left it (migrations 0
// to 7) with rows in every table that has user data.
func v8Database(t *testing.T) string {
	t.Helper()
	path := v7Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(migrations[7]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 8;
INSERT INTO delivery_attempts (at_ns, monitor, channel, event, outcome, reason) VALUES (2000, 'api', 'ops', 'monitor.down', 'sent', '');
INSERT INTO checks (position, name, url, interval_ns, timeout_ns, expected_status, kind, host, port) VALUES (1, 'db', '', 30000000000, 5000000000, 0, 'tcp', 'db.example.com', 5432);`); err != nil {
		t.Fatal(err)
	}
	return path
}

// v8Tables leaves out channels, which gains columns in v10; v9Channels
// compares its earlier columns.
var v8Tables = []string{"smtp_servers", "results", "mutes", "alert_state", "defaults", "daemon", "certs", "maintenance_windows", "channel_tests", "secret_key", "delivery_attempts"}

// v8Checks dumps the checks columns that schema v8 has.
func v8Checks(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT quote(position), quote(" + strings.ReplaceAll("name, kind, url, interval_ns, timeout_ns, expected_status, channel, alert_after, recover_after, tls_off, method, headers, body, body_key, expect_text, host, port", ", ", "), quote(") + ") FROM checks ORDER BY position")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals := make([]string, 18)
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

func TestOpenMigratesV8AddingOnlyHeartbeats(t *testing.T) {
	path := v8Database(t)
	before, beforeChecks, beforeChannels := dumpTables(t, path, v8Tables...), v8Checks(t, path), v9Channels(t, path)
	if _, err := OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("database schema version 8 is older than this up (%d)", schemaVersion)) {
		t.Fatalf("OpenReadOnly(v8) error = %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(v8) error = %v", err)
	}
	defer s.Close()
	if got := version(t, path); got != schemaVersion {
		t.Fatalf("user_version = %d, want %d", got, schemaVersion)
	}
	if after := dumpTables(t, path, v8Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("migration changed existing rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v8Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("migration changed existing checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	if after := v9Channels(t, path); !reflect.DeepEqual(beforeChannels, after) {
		t.Fatalf("migration changed existing channels:\nbefore %v\nafter  %v", beforeChannels, after)
	}
	ctx := context.Background()
	checks, err := s.Checks(ctx)
	if err != nil || len(checks) != 2 || checks[0].Period != 0 || checks[1].Kind != config.CheckTCP || checks[1].Grace != 0 {
		t.Fatalf("Checks() after migration = %+v, %v", checks, err)
	}
	// Migrated monitors keep accepting results for their unchanged definition.
	if stored, err := s.RecordDefined(ctx, checks[1], check.Result{CheckName: "db", At: t0, Status: check.StatusUp}); err != nil || !stored {
		t.Fatalf("RecordDefined() after migration = %v, %v", stored, err)
	}
	if got, err := s.APIListener(ctx); err != nil || got != (config.APIListener{}) {
		t.Fatalf("APIListener() after migration = %+v, %v", got, err)
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

// A v9 migration that fails part way (its last table name is taken) rolls
// back as a whole: the database stays v8 with its rows and no new columns.
func TestOpenV8FailingMidMigrationChangesNothing(t *testing.T) {
	path := v8Database(t)
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE daemon_api (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, beforeChecks, beforeChannels := dumpTables(t, path, v8Tables...), v8Checks(t, path), v9Channels(t, path)
	if s, err := Open(path); err == nil || !strings.Contains(err.Error(), "migrate schema 8 to 9") {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open(v8 with a clashing table) error = %v, want the 8 to 9 migration to fail", err)
	}
	if got := version(t, path); got != 8 {
		t.Fatalf("user_version = %d, want 8", got)
	}
	if after := dumpTables(t, path, v8Tables...); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed migration changed rows:\nbefore %v\nafter  %v", before, after)
	}
	if after := v8Checks(t, path); !reflect.DeepEqual(beforeChecks, after) {
		t.Fatalf("failed migration changed checks:\nbefore %v\nafter  %v", beforeChecks, after)
	}
	if after := v9Channels(t, path); !reflect.DeepEqual(beforeChannels, after) {
		t.Fatalf("failed migration changed channels:\nbefore %v\nafter  %v", beforeChannels, after)
	}
	ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Exec("SELECT period_ns FROM checks"); err == nil {
		t.Fatal("checks.period_ns exists after a failed migration")
	}
}

func TestPingStoredAfterAMissIsNewest(t *testing.T) {
	s, key := keyedStore(t)
	ctx := context.Background()
	if err := s.AddCheck(ctx, beatCheck); err != nil {
		t.Fatal(err)
	}
	tok := token(t, s, key, "backup")
	ping(t, s, tok, false, t0)
	deadline := t0.Add(beatCheck.Period + beatCheck.Grace)
	missed := check.Overdue(beatCheck, t0, t0, deadline.Add(time.Second), time.UTC)
	if stored, err := s.RecordMissed(ctx, beatCheck, t0, missed[0]); err != nil || !stored {
		t.Fatal(stored, err)
	}
	// The request began before the deadline but is stored after the miss:
	// it is stamped just after the newest result, so it is the newest.
	if !ping(t, s, tok, false, deadline.Add(-time.Second)) {
		t.Fatal("not found")
	}
	got := recent(t, s, "backup")
	if len(got) != 3 || got[0].Status != check.StatusUp || got[0].At != missed[0].At.Add(time.Nanosecond) {
		t.Fatalf("newest = %+v, want up at %v", got, missed[0].At.Add(time.Nanosecond))
	}
	// A ping after the newest result keeps its own time.
	later := deadline.Add(time.Minute)
	ping(t, s, tok, true, later)
	if got := recent(t, s, "backup"); got[0] != check.Fail("backup", later) {
		t.Fatalf("newest = %+v, want a failure at %v", got[0], later)
	}
}
