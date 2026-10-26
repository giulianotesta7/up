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

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"

	_ "modernc.org/sqlite"
)

// schemaVersion is the PRAGMA user_version this build writes and reads.
const schemaVersion = 1

const schema = `
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
`

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
	if version < schemaVersion {
		db.Close()
		return nil, fmt.Errorf("store: open %s: database is not initialized (start `up serve` first)", path)
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
	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
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
		if err := insertCheck(ctx, tx, i, c); err != nil {
			return fmt.Errorf("store: set checks: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set checks: %w", err)
	}
	return nil
}

// AddCheck appends a validated definition. An existing name is always rejected,
// even if its definition is identical. History and heartbeat are untouched.
func (s *Store) AddCheck(ctx context.Context, c config.Check) error {
	return s.appendChecks(ctx, []config.Check{c}, false)
}

// ImportChecks atomically appends validated definitions in input order without
// replacing existing definitions. Identical existing entries are no-ops; any
// conflicting existing name or repeated input name rejects the entire batch.
// Empty imports are no-ops. History and heartbeat are untouched.
func (s *Store) ImportChecks(ctx context.Context, checks []config.Check) error {
	return s.appendChecks(ctx, checks, true)
}

func (s *Store) appendChecks(ctx context.Context, checks []config.Check, importing bool) error {
	op := "add check"
	if importing {
		op = "import checks"
	}
	if err := config.ValidateChecks(checks); err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	defer tx.Rollback()
	existing, err := readChecks(ctx, tx)
	if err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	byName := make(map[string]config.Check, len(existing))
	for _, c := range existing {
		byName[c.Name] = c
	}
	var next int
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position), -1) + 1 FROM checks").Scan(&next); err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	for _, c := range checks {
		if old, ok := byName[c.Name]; ok {
			if !importing {
				return fmt.Errorf("store: %s: name %q already exists", op, c.Name)
			}
			if old != c {
				return fmt.Errorf("store: %s: name %q conflicts with existing definition", op, c.Name)
			}
			continue
		}
		if err := insertCheck(ctx, tx, next, c); err != nil {
			return fmt.Errorf("store: %s: %w", op, err)
		}
		next++
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	return nil
}

func insertCheck(ctx context.Context, tx *sql.Tx, position int, c config.Check) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO checks (position, name, url, interval_ns, timeout_ns, expected_status) VALUES (?, ?, ?, ?, ?, ?)",
		position, c.Name, c.URL, int64(c.Interval), int64(c.Timeout), c.ExpectedStatus)
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
	rows, err := reader.QueryContext(ctx, "SELECT name, url, interval_ns, timeout_ns, expected_status FROM checks ORDER BY position")
	if err != nil {
		return nil, fmt.Errorf("store: checks: %w", err)
	}
	defer rows.Close()
	var checks []config.Check
	for rows.Next() {
		var c config.Check
		var interval, timeout int64
		if err := rows.Scan(&c.Name, &c.URL, &interval, &timeout, &c.ExpectedStatus); err != nil {
			return nil, fmt.Errorf("store: checks: %w", err)
		}
		c.Interval, c.Timeout = time.Duration(interval), time.Duration(timeout)
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
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
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
