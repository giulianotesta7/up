package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

// Defaults returns the stored defaults, or config.BuiltinDefaults when none
// were saved. A default channel that was deleted reads as none (DeleteChannel
// clears it).
func (s *Store) Defaults(ctx context.Context) (config.Defaults, error) {
	var d config.Defaults
	var interval, timeout int64
	err := s.db.QueryRowContext(ctx, "SELECT zone, channel, interval_ns, timeout_ns, expected_status, alert_after, recover_after FROM defaults WHERE id = 1").
		Scan(&d.Zone, &d.Channel, &interval, &timeout, &d.ExpectedStatus, &d.AlertAfter, &d.RecoverAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return config.BuiltinDefaults(), nil
	}
	if err != nil {
		return config.Defaults{}, fmt.Errorf("store: defaults: %w", err)
	}
	d.Interval, d.Timeout = time.Duration(interval), time.Duration(timeout)
	return d, nil
}

// SetDefaults validates and replaces the defaults. An invalid value or a
// channel that does not exist is ErrValidation and stores nothing. Existing
// monitors, channels and windows are never touched.
func (s *Store) SetDefaults(ctx context.Context, d config.Defaults) error {
	if err := config.ValidateDefaults(d); err != nil {
		return classify(ErrValidation, fmt.Errorf("store: set defaults: %w", err))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: set defaults: %w", err)
	}
	defer tx.Rollback()
	if err := requireChannel(ctx, tx, d.Channel); err != nil {
		return fmt.Errorf("store: set defaults: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO defaults (id, zone, channel, interval_ns, timeout_ns, expected_status, alert_after, recover_after)
VALUES (1, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET zone = excluded.zone, channel = excluded.channel, interval_ns = excluded.interval_ns,
	timeout_ns = excluded.timeout_ns, expected_status = excluded.expected_status,
	alert_after = excluded.alert_after, recover_after = excluded.recover_after`,
		d.Zone, d.Channel, int64(d.Interval), int64(d.Timeout), d.ExpectedStatus, d.AlertAfter, d.RecoverAfter); err != nil {
		return fmt.Errorf("store: set defaults: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: set defaults: %w", err)
	}
	return nil
}
