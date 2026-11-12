package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/heohlee/up/internal/config"
)

// SetPingPermission records whether the running daemon can send pings, as
// detected at its start. Only config.PingAllowed and config.PingDenied are
// stored.
func (s *Store) SetPingPermission(ctx context.Context, p config.PingPermission) error {
	if p != config.PingAllowed && p != config.PingDenied {
		return classify(ErrValidation, fmt.Errorf("store: set ping permission: %q must be %q or %q", p, config.PingAllowed, config.PingDenied))
	}
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO daemon_pings (id, permission) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET permission = excluded.permission", string(p))
	if err != nil {
		return fmt.Errorf("store: set ping permission: %w", err)
	}
	return nil
}

// PingPermission returns what the daemon last reported at its start;
// config.PingUnknown when it never did.
func (s *Store) PingPermission(ctx context.Context) (config.PingPermission, error) {
	var p string
	err := s.db.QueryRowContext(ctx, "SELECT permission FROM daemon_pings WHERE id = 1").Scan(&p)
	if errors.Is(err, sql.ErrNoRows) {
		return config.PingUnknown, nil
	}
	if err != nil {
		return config.PingUnknown, fmt.Errorf("store: ping permission: %w", err)
	}
	return config.PingPermission(p), nil
}
