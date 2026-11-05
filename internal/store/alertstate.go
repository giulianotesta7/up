package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// AlertState returns whether the recipients were last told that the check is
// down, and false for found when nothing was recorded yet.
func (s *Store) AlertState(ctx context.Context, name string) (known, found bool, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT known_alerting FROM alert_state WHERE check_name = ?", name).Scan(&known)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("store: alert state: %w", err)
	}
	return known, true, nil
}

// SetAlertState records what the recipients now know about an existing check.
// An unknown check is ErrNotFound and records nothing.
func (s *Store) SetAlertState(ctx context.Context, name string, known bool) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO alert_state (check_name, known_alerting) SELECT ?, ? WHERE EXISTS (SELECT 1 FROM checks WHERE name = ?) ON CONFLICT(check_name) DO UPDATE SET known_alerting = excluded.known_alerting`, name, known, name)
	if err != nil {
		return fmt.Errorf("store: set alert state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set alert state: %w", err)
	}
	if n == 0 {
		return classify(ErrNotFound, fmt.Errorf("store: set alert state: name %q not found", name))
	}
	return nil
}

// AlertStates returns every recorded state by check name.
func (s *Store) AlertStates(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT check_name, known_alerting FROM alert_state")
	if err != nil {
		return nil, fmt.Errorf("store: alert states: %w", err)
	}
	defer rows.Close()
	states := map[string]bool{}
	for rows.Next() {
		var name string
		var known bool
		if err := rows.Scan(&name, &known); err != nil {
			return nil, fmt.Errorf("store: alert states: %w", err)
		}
		states[name] = known
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: alert states: %w", err)
	}
	return states, nil
}
