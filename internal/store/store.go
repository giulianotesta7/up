// Package store keeps check definitions, check results and the daemon
// heartbeat in SQLite. The daemon opens it read-write; the TUI opens it
// read-only and polls it.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"

	_ "modernc.org/sqlite"
)

// schemaVersion is the PRAGMA user_version this build writes and reads.
const schemaVersion = 12

// migrations[v] upgrades a database from schema version v to v+1. A new
// database runs all of them in one transaction. Never edit a released entry:
// append a new one instead.
var migrations = []string{
	// v0 -> v1: checks, results and the daemon heartbeat.
	`
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
`,
	// v1 -> v2: SMTP servers, alert channels, the monitor's channel and the
	// public key that encrypts SMTP passwords. Kinds and TLS modes are
	// validated in Go, so new values need no table rebuild.
	`
ALTER TABLE checks ADD COLUMN channel TEXT NOT NULL DEFAULT '';
CREATE TABLE smtp_servers (
	name         TEXT    NOT NULL PRIMARY KEY,
	host         TEXT    NOT NULL,
	port         INTEGER NOT NULL,
	tls          TEXT    NOT NULL,
	username     TEXT    NOT NULL,
	password     BLOB    NOT NULL,
	password_key BLOB    NOT NULL,
	sender       TEXT    NOT NULL
);
CREATE TABLE channels (
	name        TEXT NOT NULL PRIMARY KEY,
	kind        TEXT NOT NULL,
	smtp_server TEXT NOT NULL,
	recipients  TEXT NOT NULL
);
CREATE TABLE secret_key (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	public_key BLOB    NOT NULL
);
`,
	// v2 -> v3: consecutive failures before the down alert and consecutive
	// successes before the recovery alert. 0 means 1 (the previous behavior).
	`
ALTER TABLE checks ADD COLUMN alert_after   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checks ADD COLUMN recover_after INTEGER NOT NULL DEFAULT 0;
`,
	// v3 -> v4: temporary alert mutes.
	`CREATE TABLE mutes (check_name TEXT PRIMARY KEY, until_ns INTEGER NOT NULL, was_alerting INTEGER NOT NULL);`,
	// v4 -> v5: last observed TLS certificates.
	`
CREATE TABLE certs (check_name TEXT PRIMARY KEY, host TEXT NOT NULL, issuer TEXT NOT NULL, serial TEXT NOT NULL, not_after_ns INTEGER NOT NULL, observed_ns INTEGER NOT NULL, notified INTEGER NOT NULL);
ALTER TABLE checks ADD COLUMN tls_off INTEGER NOT NULL DEFAULT 0;
`,
	// v5 -> v6: shared foundation for HTTP request options, TCP checks,
	// webhook channels, maintenance windows and channel test requests.
	// Existing rows keep today's behavior: kind http, empty method (GET), no
	// headers, body or expected text, and email channels without webhook
	// fields. Headers are a JSON list of {n, v, k} (name, sealed value, key);
	// body, webhook URL and webhook auth value are sealed like SMTP
	// passwords. Window weekdays are a bit set (bit 0 is Sunday). alert_state
	// is what the recipients were last told about each check (down or not);
	// deleting a check deletes its row.
	`
ALTER TABLE checks ADD COLUMN kind        TEXT    NOT NULL DEFAULT 'http';
ALTER TABLE checks ADD COLUMN method      TEXT    NOT NULL DEFAULT '';
ALTER TABLE checks ADD COLUMN headers     TEXT    NOT NULL DEFAULT '';
ALTER TABLE checks ADD COLUMN body        BLOB    NOT NULL DEFAULT x'';
ALTER TABLE checks ADD COLUMN body_key    BLOB    NOT NULL DEFAULT x'';
ALTER TABLE checks ADD COLUMN expect_text TEXT    NOT NULL DEFAULT '';
ALTER TABLE checks ADD COLUMN host        TEXT    NOT NULL DEFAULT '';
ALTER TABLE checks ADD COLUMN port        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE channels ADD COLUMN webhook_origin    TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN webhook_url       BLOB NOT NULL DEFAULT x'';
ALTER TABLE channels ADD COLUMN webhook_url_key   BLOB NOT NULL DEFAULT x'';
ALTER TABLE channels ADD COLUMN webhook_auth_name TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN webhook_auth      BLOB NOT NULL DEFAULT x'';
ALTER TABLE channels ADD COLUMN webhook_auth_key  BLOB NOT NULL DEFAULT x'';
CREATE TABLE maintenance_windows (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	check_name   TEXT    NOT NULL,
	weekdays     INTEGER NOT NULL,
	start_minute INTEGER NOT NULL,
	duration_ns  INTEGER NOT NULL,
	zone         TEXT    NOT NULL
);
CREATE INDEX maintenance_windows_by_check ON maintenance_windows (check_name);
CREATE TABLE channel_tests (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	channel      TEXT    NOT NULL,
	status       TEXT    NOT NULL CHECK (status IN ('pending', 'sending', 'ok', 'error')),
	error        TEXT    NOT NULL,
	requested_ns INTEGER NOT NULL,
	started_ns   INTEGER NOT NULL,
	finished_ns  INTEGER NOT NULL
);
CREATE UNIQUE INDEX channel_tests_one_active ON channel_tests (channel) WHERE status IN ('pending', 'sending');
CREATE TABLE alert_state (check_name TEXT PRIMARY KEY, known_alerting INTEGER NOT NULL);
CREATE TRIGGER alert_state_with_check AFTER DELETE ON checks BEGIN DELETE FROM alert_state WHERE check_name = OLD.name; END;
`,
	// v6 -> v7: the defaults the new monitor and new maintenance window forms
	// start from. A single row (id = 1) holds them; no row means the built-in
	// values (config.BuiltinDefaults), so upgrading changes nothing. An empty
	// channel means none.
	`
CREATE TABLE defaults (
	id              INTEGER PRIMARY KEY CHECK (id = 1),
	zone            TEXT    NOT NULL,
	channel         TEXT    NOT NULL,
	interval_ns     INTEGER NOT NULL,
	timeout_ns      INTEGER NOT NULL,
	expected_status INTEGER NOT NULL,
	alert_after     INTEGER NOT NULL,
	recover_after   INTEGER NOT NULL
);
`,
	// v7 -> v8: the log of alert delivery attempts (monitor.down,
	// monitor.recovered, cert.expiring), one row per monitor and channel.
	// Additive: no existing table or row changes. Events and outcomes are
	// validated in Go; the reason is short and secret-free.
	`
CREATE TABLE delivery_attempts (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	at_ns   INTEGER NOT NULL,
	monitor TEXT    NOT NULL,
	channel TEXT    NOT NULL,
	event   TEXT    NOT NULL,
	outcome TEXT    NOT NULL CHECK (outcome IN ('sent', 'failed', 'held')),
	reason  TEXT    NOT NULL
);
CREATE INDEX delivery_attempts_by_time ON delivery_attempts (at_ns);
CREATE INDEX delivery_attempts_by_channel ON delivery_attempts (channel, at_ns);
CREATE INDEX delivery_attempts_by_monitor ON delivery_attempts (monitor, at_ns);
`,
	// v8 -> v9: heartbeat monitors and the daemon's API address. Additive:
	// existing rows read period and grace 0 and no token (HTTP and TCP checks
	// have none). The ping token is sealed like webhook URLs so it can be
	// shown again, and its SHA-256 hash finds the monitor without opening any
	// token. daemon_api is one row (id = 1) written by every `up serve` start;
	// an empty listen means the daemon has no API.
	`
ALTER TABLE checks ADD COLUMN period_ns       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checks ADD COLUMN grace_ns        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE checks ADD COLUMN ping_token      BLOB    NOT NULL DEFAULT x'';
ALTER TABLE checks ADD COLUMN ping_token_key  BLOB    NOT NULL DEFAULT x'';
ALTER TABLE checks ADD COLUMN ping_token_hash BLOB    NOT NULL DEFAULT x'';
CREATE UNIQUE INDEX checks_by_ping_token ON checks (ping_token_hash) WHERE ping_token_hash != x'';
CREATE TABLE daemon_api (
	id     INTEGER PRIMARY KEY CHECK (id = 1),
	listen TEXT    NOT NULL,
	tls    INTEGER NOT NULL
);
`,
	// v9 -> v10: ntfy, telegram, discord, slack and gotify channels.
	// Additive: existing channels read no push fields. Discord and slack keep
	// their URL in the webhook columns; token is the ntfy access token, the
	// telegram bot token or the gotify app token, sealed like webhook URLs.
	`
ALTER TABLE channels ADD COLUMN server_url TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN topic      TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN chat_id    TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN token      BLOB NOT NULL DEFAULT x'';
ALTER TABLE channels ADD COLUMN token_key  BLOB NOT NULL DEFAULT x'';
ALTER TABLE channels ADD COLUMN priority   TEXT NOT NULL DEFAULT '';
`,
	// v10 -> v11: HTTP response conditions. Additive: existing rows read an
	// empty list. Conditions are a JSON list of {k, p, n, v, r, w} (kind,
	// JSON path, header name, value, regex pattern, max response time in
	// nanoseconds) in check order; they hold no secrets.
	`ALTER TABLE checks ADD COLUMN conditions TEXT NOT NULL DEFAULT '';`,
	// v11 -> v12: heartbeat cron schedules. Additive: existing rows read an
	// empty schedule (period) and no cron expression, so existing heartbeats
	// keep their period. schedule is '' or 'cron'; cron is the expression
	// with its fields separated by one space.
	`
ALTER TABLE checks ADD COLUMN schedule TEXT NOT NULL DEFAULT '';
ALTER TABLE checks ADD COLUMN cron     TEXT NOT NULL DEFAULT '';
`,
}

// Outage is a period during which a check was down. Ongoing outages have a
// zero End.
type Outage struct {
	Start   time.Time
	End     time.Time
	Ongoing bool
	// Error is the reason reported by the first failed check of the outage.
	Error string
}

// Store is a handle to the up database.
type Store struct {
	db *sql.DB
}

// Open opens the database at path for writing, creating and migrating it
// when needed.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// One connection serializes writes from the daemon's check goroutines.
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// OpenReadOnly opens an existing database without write access. It fails
// when the database does not exist.
func OpenReadOnly(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	version, err := userVersion(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := checkVersion(version); err != nil {
		db.Close()
		return nil, err
	}
	if version == 0 {
		db.Close()
		return nil, fmt.Errorf("store: open %s: database is not initialized (start `up serve` first)", path)
	}
	if version < schemaVersion {
		db.Close()
		return nil, fmt.Errorf("store: open %s: database schema version %d is older than this up (%d); open it with `up serve` or `up tui` to upgrade it", path, version, schemaVersion)
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error {
	return s.db.Close()
}

func userVersion(db *sql.DB) (int, error) {
	var v int
	err := db.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

func checkVersion(v int) error {
	if v > schemaVersion {
		return fmt.Errorf("store: database schema version %d is newer than this up supports (%d)", v, schemaVersion)
	}
	return nil
}

func migrate(db *sql.DB) error {
	version, err := userVersion(db)
	if err != nil {
		return err
	}
	if err := checkVersion(version); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for v := version; v < schemaVersion; v++ {
		if _, err := tx.Exec(migrations[v]); err != nil {
			if v == 0 {
				return fmt.Errorf("create schema: %w", err)
			}
			return fmt.Errorf("migrate schema %d to %d: %w", v, v+1, err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// SetChecks replaces the configured checks, keeping their order. Results of
// removed checks are kept.
func (s *Store) SetChecks(ctx context.Context, checks []config.Check) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set checks: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM checks"); err != nil {
		return fmt.Errorf("store: set checks: %w", err)
	}
	for i, c := range checks {
		if err := insertCheck(ctx, tx, i, c, nil); err != nil {
			return fmt.Errorf("store: set checks: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set checks: %w", err)
	}
	return nil
}

// AddCheck appends a validated definition. An existing name is rejected, even
// if its definition is identical. History and heartbeat are untouched.
func (s *Store) AddCheck(ctx context.Context, c config.Check) error {
	return s.addCheck(ctx, "", c)
}

// AddCheckFrom adds c like AddCheck, taking the protected values it keeps
// (EditKeep) from the stored monitor named source instead of from c: the
// sealed values are copied inside the store and never decrypted. Replace and
// clear still override them. A missing source stores nothing and fails with
// ErrNotFound. A TCP check has no protected values and keeps nothing.
func (s *Store) AddCheckFrom(ctx context.Context, source string, c config.Check) error {
	if source == "" {
		return classify(ErrValidation, errors.New("store: add check: source monitor is required"))
	}
	return s.addCheck(ctx, source, c)
}

func (s *Store) addCheck(ctx context.Context, source string, c config.Check) error {
	if err := config.ValidateCheck(c); err != nil {
		return classify(ErrValidation, fmt.Errorf("store: add check: %w", err))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: add check: %w", err)
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM checks WHERE name = ?)", c.Name).Scan(&exists); err != nil {
		return fmt.Errorf("store: add check: %w", err)
	}
	if exists {
		return classify(ErrExists, fmt.Errorf("store: add check: name %q already exists", c.Name))
	}
	var from *protectedValues
	if source != "" {
		values, found, err := storedProtected(ctx, tx, source)
		if err != nil {
			return fmt.Errorf("store: add check: %w", err)
		}
		if !found {
			return classify(ErrNotFound, fmt.Errorf("store: add check: source monitor %q not found", source))
		}
		if c.Kind != config.CheckTCP {
			from = &values
		}
	}
	var next int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position), -1) + 1 FROM checks").Scan(&next); err != nil {
		return fmt.Errorf("store: add check: %w", err)
	}
	if err := insertCheck(ctx, tx, next, c, from); err != nil {
		return fmt.Errorf("store: add check: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: add check: %w", err)
	}
	return nil
}

// insertCheck stores c; from, when set, supplies the values c keeps.
func insertCheck(ctx context.Context, tx *sql.Tx, position int, c config.Check, from *protectedValues) error {
	if err := requireChannel(ctx, tx, c.Channel); err != nil {
		return err
	}
	c = c.Canonical()
	f, err := checkFields(c, from)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx,
		"INSERT INTO checks (position, "+checkColumns+") VALUES (?, "+checkPlaceholders+")",
		append([]any{position}, f.values(c)...)...); err != nil {
		return err
	}
	if c.Kind == config.CheckHeartbeat {
		_, err = setPingToken(ctx, tx, c.Name)
	}
	return err
}

// Checks returns the configured checks in configuration order.
func (s *Store) Checks(ctx context.Context) ([]config.Check, error) {
	return readChecks(ctx, s.db)
}

type checkReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readChecks(ctx context.Context, reader checkReader) ([]config.Check, error) {
	rows, err := reader.QueryContext(ctx, "SELECT "+checkColumns+" FROM checks ORDER BY position")
	if err != nil {
		return nil, fmt.Errorf("store: checks: %w", err)
	}
	defer rows.Close()
	var checks []config.Check
	for rows.Next() {
		c, err := scanCheck(rows)
		if err != nil {
			return nil, fmt.Errorf("store: checks: %w", err)
		}
		checks = append(checks, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: checks: %w", err)
	}
	return checks, nil
}

// Record stores one check result. Canceled results say nothing about the
// service and are refused.
func (s *Store) Record(ctx context.Context, r check.Result) error {
	if r.Status != check.StatusUp && r.Status != check.StatusDown {
		return fmt.Errorf("store: refusing to record status %q for %s", r.Status, r.CheckName)
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error) VALUES (?, ?, ?, ?, ?, ?)",
		r.CheckName, r.At.UnixNano(), string(r.Status), int64(r.Latency), r.StatusCode, r.Error)
	if err != nil {
		return fmt.Errorf("store: record %s: %w", r.CheckName, err)
	}
	return nil
}

// Prune deletes results older than before and returns how many it removed.
// For each check it keeps the newest result older than before, so the status
// history that remains still starts from the state in force at the cutoff
// (an ongoing outage keeps its start, a check silent for longer than the
// window keeps its last result). Definitions and the heartbeat are untouched.
//
// Delivery attempts older than before are deleted too (they keep nothing
// back); the count is of results only.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: prune: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
DELETE FROM results
 WHERE at_ns < ?1
   AND id NOT IN (SELECT id FROM (
         SELECT id, ROW_NUMBER() OVER (PARTITION BY check_name ORDER BY at_ns DESC, id DESC) AS rn
           FROM results WHERE at_ns < ?1) WHERE rn = 1)`, before.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("store: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM delivery_attempts WHERE at_ns < ?", before.UnixNano()); err != nil {
		return 0, fmt.Errorf("store: prune: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: prune: %w", err)
	}
	return n, nil
}

// Latest returns the newest result of a check; ok is false when it has none.
func (s *Store) Latest(ctx context.Context, name string) (r check.Result, ok bool, err error) {
	results, err := s.Recent(ctx, name, 1)
	if err != nil || len(results) == 0 {
		return check.Result{}, false, err
	}
	return results[0], true, nil
}

// Recent returns up to limit results of a check, newest first.
func (s *Store) Recent(ctx context.Context, name string, limit int) ([]check.Result, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT at_ns, status, latency_ns, status_code, error FROM results WHERE check_name = ? ORDER BY at_ns DESC, id DESC LIMIT ?",
		name, limit)
	if err != nil {
		return nil, fmt.Errorf("store: recent %s: %w", name, err)
	}
	defer rows.Close()
	var results []check.Result
	for rows.Next() {
		r := check.Result{CheckName: name}
		var at, latency int64
		var status string
		if err := rows.Scan(&at, &status, &latency, &r.StatusCode, &r.Error); err != nil {
			return nil, fmt.Errorf("store: recent %s: %w", name, err)
		}
		r.At, r.Status, r.Latency = fromNanos(at), check.Status(status), time.Duration(latency)
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: recent %s: %w", name, err)
	}
	return results, nil
}

// Outages returns up to limit outages of a check, newest first. An outage
// starts at a down result that follows an up result (or is the first
// result) and ends at the next up result.
func (s *Store) Outages(ctx context.Context, name string, limit int) ([]Outage, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT o.at_ns, o.error,
       (SELECT MIN(r.at_ns) FROM results r
         WHERE r.check_name = ?1 AND r.status = 'up' AND r.at_ns > o.at_ns) AS end_ns
  FROM (SELECT at_ns, status, error,
               LAG(status) OVER (ORDER BY at_ns, id) AS prev
          FROM results WHERE check_name = ?1) o
 WHERE o.status = 'down' AND (o.prev IS NULL OR o.prev = 'up')
 ORDER BY o.at_ns DESC
 LIMIT ?2`, name, limit)
	if err != nil {
		return nil, fmt.Errorf("store: outages %s: %w", name, err)
	}
	defer rows.Close()
	var outages []Outage
	for rows.Next() {
		var o Outage
		var start int64
		var end sql.NullInt64
		if err := rows.Scan(&start, &o.Error, &end); err != nil {
			return nil, fmt.Errorf("store: outages %s: %w", name, err)
		}
		o.Start = fromNanos(start)
		if end.Valid {
			o.End = fromNanos(end.Int64)
		} else {
			o.Ongoing = true
		}
		outages = append(outages, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: outages %s: %w", name, err)
	}
	return outages, nil
}

// Beat records that the daemon is alive at the given time.
func (s *Store) Beat(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO daemon (id, last_beat_ns) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET last_beat_ns = excluded.last_beat_ns",
		at.UnixNano())
	if err != nil {
		return fmt.Errorf("store: beat: %w", err)
	}
	return nil
}

// LastBeat returns the daemon's last heartbeat; ok is false when it never
// ran.
func (s *Store) LastBeat(ctx context.Context) (at time.Time, ok bool, err error) {
	var ns int64
	err = s.db.QueryRowContext(ctx, "SELECT last_beat_ns FROM daemon WHERE id = 1").Scan(&ns)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("store: last beat: %w", err)
	}
	return fromNanos(ns), true, nil
}

func fromNanos(ns int64) time.Time {
	return time.Unix(0, ns).UTC()
}
