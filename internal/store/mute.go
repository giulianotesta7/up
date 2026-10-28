package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

// SetMute creates or extends a mute, retaining its original notification state.
func (s *Store) SetMute(ctx context.Context, name string, until time.Time, wasAlerting bool) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO mutes (check_name, until_ns, was_alerting) SELECT ?, ?, ? WHERE EXISTS (SELECT 1 FROM checks WHERE name = ?) ON CONFLICT(check_name) DO UPDATE SET until_ns = excluded.until_ns`, name, until.UnixNano(), wasAlerting, name)
	if err != nil {
		return fmt.Errorf("store: set mute: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set mute: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: set mute: name %q not found", name)
	}
	return nil
}

// Unmute expires a mute without losing its notification state.
func (s *Store) Unmute(ctx context.Context, name string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE mutes SET until_ns = ? WHERE check_name = ?", at.UnixNano(), name)
	if err != nil {
		return fmt.Errorf("store: unmute: %w", err)
	}
	return nil
}

// ClearMute removes a reconciled mute, but only if it ended at or before
// notAfter, so a mute extended meanwhile survives.
func (s *Store) ClearMute(ctx context.Context, name string, notAfter time.Time) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM mutes WHERE check_name = ? AND until_ns <= ?", name, notAfter.UnixNano())
	if err != nil {
		return fmt.Errorf("store: clear mute: %w", err)
	}
	return nil
}

// Mute returns a check's mute, if present.
func (s *Store) Mute(ctx context.Context, name string) (config.Mute, bool, error) {
	var m config.Mute
	var ns int64
	err := s.db.QueryRowContext(ctx, "SELECT check_name, until_ns, was_alerting FROM mutes WHERE check_name = ?", name).Scan(&m.Check, &ns, &m.WasAlerting)
	if errors.Is(err, sql.ErrNoRows) {
		return config.Mute{}, false, nil
	}
	if err != nil {
		return config.Mute{}, false, fmt.Errorf("store: mute: %w", err)
	}
	m.Until = fromNanos(ns)
	return m, true, nil
}

// Mutes returns all mutes ordered by check name.
func (s *Store) Mutes(ctx context.Context) ([]config.Mute, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT check_name, until_ns, was_alerting FROM mutes ORDER BY check_name")
	if err != nil {
		return nil, fmt.Errorf("store: mutes: %w", err)
	}
	defer rows.Close()
	var result []config.Mute
	for rows.Next() {
		var m config.Mute
		var ns int64
		if err := rows.Scan(&m.Check, &ns, &m.WasAlerting); err != nil {
			return nil, fmt.Errorf("store: mutes: %w", err)
		}
		m.Until = fromNanos(ns)
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: mutes: %w", err)
	}
	return result, nil
}
