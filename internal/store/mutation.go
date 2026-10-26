package store

import (
	"context"
	"fmt"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
)

// UpdateCheck replaces a validated definition's mutable fields. Its name,
// position, history and the daemon heartbeat are left untouched.
func (s *Store) UpdateCheck(ctx context.Context, c config.Check) error {
	if err := config.ValidateCheck(c); err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE checks
SET url = ?, interval_ns = ?, timeout_ns = ?, expected_status = ? WHERE name = ?`,
		c.URL, int64(c.Interval), int64(c.Timeout), c.ExpectedStatus, c.Name)
	if err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: update check: name %q not found", c.Name)
	}
	return nil
}

// DeleteCheck atomically removes a definition and all its history. A missing
// name is rejected without changing history, other definitions or heartbeat.
func (s *Store) DeleteCheck(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM checks WHERE name = ?", name)
	if err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: delete check: name %q not found", name)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM results WHERE check_name = ?", name); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	return nil
}

// RecordDefined stores a result only while its definition exists. The existence
// check and insert share one statement, serialized with DeleteCheck even across
// database handles/processes. Record intentionally still allows undefined names.
func (s *Store) RecordDefined(ctx context.Context, r check.Result) (bool, error) {
	if r.Status != check.StatusUp && r.Status != check.StatusDown {
		return false, fmt.Errorf("store: refusing to record status %q for %s", r.Status, r.CheckName)
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error)
SELECT ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM checks WHERE name = ?)`,
		r.CheckName, r.At.UnixNano(), string(r.Status), int64(r.Latency), r.StatusCode, r.Error, r.CheckName)
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", r.CheckName, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", r.CheckName, err)
	}
	return n > 0, nil
}
