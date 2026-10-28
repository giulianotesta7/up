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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	defer tx.Rollback()
	if err := requireChannel(ctx, tx, c.Channel); err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE checks
SET url = ?, interval_ns = ?, timeout_ns = ?, expected_status = ?, channel = ?, alert_after = ?, recover_after = ? WHERE name = ?`,
		c.URL, int64(c.Interval), int64(c.Timeout), c.ExpectedStatus, c.Channel, c.AlertAfter, c.RecoverAfter, c.Name)
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
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: update check: %w", err)
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
	if _, err := tx.ExecContext(ctx, "DELETE FROM mutes WHERE check_name = ?", name); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM certs WHERE check_name = ?", name); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	return nil
}

// RecordDefined stores a result only while the definition it measured still
// exists unchanged. The comparison and insert share one statement, serialized
// with mutations even across database handles/processes. Schema v1 cannot detect
// deletion and recreation with an identical definition during an in-flight
// check; that measurement of the same definition may enter the fresh history.
// Record intentionally still allows undefined names.
func (s *Store) RecordDefined(ctx context.Context, c config.Check, r check.Result) (bool, error) {
	if r.Status != check.StatusUp && r.Status != check.StatusDown {
		return false, fmt.Errorf("store: refusing to record status %q for %s", r.Status, r.CheckName)
	}
	if r.CheckName != c.Name {
		return false, nil
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error)
SELECT ?, ?, ?, ?, ?, ? WHERE EXISTS (
SELECT 1 FROM checks WHERE name = ? AND url = ? AND interval_ns = ? AND timeout_ns = ? AND expected_status = ?)`,
		r.CheckName, r.At.UnixNano(), string(r.Status), int64(r.Latency), r.StatusCode, r.Error,
		c.Name, c.URL, int64(c.Interval), int64(c.Timeout), c.ExpectedStatus)
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", r.CheckName, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", r.CheckName, err)
	}
	return n > 0, nil
}
