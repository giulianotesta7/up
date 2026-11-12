package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
)

// UpdateCheck replaces a validated definition's mutable fields. Its name,
// position, history and the daemon heartbeat are left untouched. A tag it
// drops that no other check carries loses its mutes and windows.
func (s *Store) UpdateCheck(ctx context.Context, c config.Check) error {
	if err := config.ValidateCheck(c); err != nil {
		return classify(ErrValidation, fmt.Errorf("store: update check: %w", err))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	defer tx.Rollback()
	if err := requireChannel(ctx, tx, c.Channel); err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	c = c.Canonical()
	// A missing name finds nothing to keep and fails below with ErrNotFound.
	stored, _, err := storedProtected(ctx, tx, c.Name)
	if err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	f, err := checkFields(c, &stored)
	if err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE checks
SET kind = ?, url = ?, interval_ns = ?, timeout_ns = ?, expected_status = ?, channel = ?, alert_after = ?, recover_after = ?, tls_off = ?,
    method = ?, headers = ?, body = ?, body_key = ?, expect_text = ?, host = ?, port = ?, period_ns = ?, grace_ns = ?, conditions = ?, tags = ? WHERE name = ?`,
		f.kind, c.URL, int64(c.Interval), int64(c.Timeout), c.ExpectedStatus, c.Channel, c.AlertAfter, c.RecoverAfter, c.TLSOff,
		c.Method, f.headers, blob(f.body.Data), blob(f.body.Key), c.ExpectText, c.Host, c.Port, int64(c.Period), int64(c.Grace), f.conditions, encodeTags(c.Tags), c.Name)
	if err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	if n == 0 {
		return classify(ErrNotFound, fmt.Errorf("store: update check: name %q not found", c.Name))
	}
	// A heartbeat keeps its token across edits; a check that becomes one
	// gets a token and one that stops being one loses it.
	if err := keepPingToken(ctx, tx, c); err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	if err := pruneTags(ctx, tx); err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: update check: %w", err)
	}
	return nil
}

// DeleteCheck atomically removes a definition and all its history, and the
// mutes and windows of the tags no other check carries. A missing name is
// rejected without changing history, other definitions or heartbeat.
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
		return classify(ErrNotFound, fmt.Errorf("store: delete check: name %q not found", name))
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM results WHERE check_name = ?", name); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM delivery_attempts WHERE monitor = ?", name); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM mutes WHERE check_name = ?", name); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM certs WHERE check_name = ?", name); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM maintenance_windows WHERE check_name = ?", name); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	if err := pruneTags(ctx, tx); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: delete check: %w", err)
	}
	return nil
}

// RecordDefined stores a result only while the definition it measured still
// exists unchanged. The comparison and insert share one statement, serialized
// with mutations even across database handles/processes. The comparison covers
// every field the check executes with: URL, kind, method, headers, body,
// expected text, conditions, host, port, interval, timeout, expected status,
// period and grace. It must
// stay in step with config.Check.SameDefinition (channel and alert settings
// do not change a measurement, so they are not compared). Schema v1 cannot
// detect deletion and recreation with an identical definition during an
// in-flight check; that measurement of the same definition may enter the fresh
// history. Record intentionally still allows undefined names.
func (s *Store) RecordDefined(ctx context.Context, c config.Check, r check.Result) (bool, error) {
	if r.Status != check.StatusUp && r.Status != check.StatusDown {
		return false, fmt.Errorf("store: refusing to record status %q for %s", r.Status, r.CheckName)
	}
	if r.CheckName != c.Name {
		return false, nil
	}
	c = c.Canonical()
	headers, err := encodeHeaders(c.Headers)
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", r.CheckName, err)
	}
	conds, err := encodeConditions(c.Conditions)
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", r.CheckName, err)
	}
	kind := c.Kind
	if kind == "" {
		kind = config.CheckHTTP
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error)
SELECT ?, ?, ?, ?, ?, ? WHERE EXISTS (
SELECT 1 FROM checks WHERE name = ? AND url = ? AND interval_ns = ? AND timeout_ns = ? AND expected_status = ?
  AND kind = ? AND method = ? AND headers = ? AND body = ? AND body_key = ? AND expect_text = ? AND host = ? AND port = ?
  AND period_ns = ? AND grace_ns = ? AND conditions = ?)`,
		r.CheckName, r.At.UnixNano(), string(r.Status), int64(r.Latency), r.StatusCode, r.Error,
		c.Name, c.URL, int64(c.Interval), int64(c.Timeout), c.ExpectedStatus,
		kind, c.Method, headers, blob(c.Body.Data), blob(c.Body.Key), c.ExpectText, c.Host, c.Port, int64(c.Period), int64(c.Grace), conds)
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", r.CheckName, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", r.CheckName, err)
	}
	return n > 0, nil
}

// storedProtected reads the protected values of the check called name and
// whether it exists.
func storedProtected(ctx context.Context, tx *sql.Tx, name string) (protectedValues, bool, error) {
	var stored protectedValues
	var headers string
	var body, bodyKey []byte
	err := tx.QueryRowContext(ctx, "SELECT headers, body, body_key FROM checks WHERE name = ?", name).Scan(&headers, &body, &bodyKey)
	if errors.Is(err, sql.ErrNoRows) {
		return protectedValues{}, false, nil
	}
	if err != nil {
		return protectedValues{}, false, err
	}
	if stored.headers, err = decodeHeaders(headers); err != nil {
		return protectedValues{}, false, err
	}
	if len(body) > 0 {
		stored.body = secret.Sealed{Key: bodyKey, Data: body}
	}
	return stored, true, nil
}
