package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

// MaxMaintenanceWindows is how many windows one check may have.
const MaxMaintenanceWindows = 20

const windowColumns = "id, check_name, weekdays, start_minute, duration_ns, zone"

func scanWindow(row scanner) (config.MaintenanceWindow, error) {
	var w config.MaintenanceWindow
	var mask int
	var duration int64
	if err := row.Scan(&w.ID, &w.Check, &mask, &w.StartMinute, &duration, &w.Zone); err != nil {
		return config.MaintenanceWindow{}, err
	}
	w.Weekdays, w.Duration = config.WeekdaysFromMask(mask), time.Duration(duration)
	return w, nil
}

func canonicalWindow(w config.MaintenanceWindow) config.MaintenanceWindow {
	w.Weekdays = slices.Clone(w.Weekdays)
	slices.Sort(w.Weekdays)
	return w
}

// AddMaintenanceWindow stores a validated window (at most MaxMaintenanceWindows
// per check) and returns it with its ID and weekdays sorted. An unknown check
// is ErrNotFound; an invalid window or a full check is ErrValidation.
func (s *Store) AddMaintenanceWindow(ctx context.Context, w config.MaintenanceWindow) (config.MaintenanceWindow, error) {
	if err := config.ValidateMaintenanceWindow(w); err != nil {
		return config.MaintenanceWindow{}, classify(ErrValidation, fmt.Errorf("store: add maintenance window: %w", err))
	}
	w = canonicalWindow(w)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return config.MaintenanceWindow{}, fmt.Errorf("store: add maintenance window: %w", err)
	}
	defer tx.Rollback()
	var exists bool
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM checks WHERE name = ?), (SELECT COUNT(*) FROM maintenance_windows WHERE check_name = ?)", w.Check, w.Check).Scan(&exists, &count); err != nil {
		return config.MaintenanceWindow{}, fmt.Errorf("store: add maintenance window: %w", err)
	}
	if !exists {
		return config.MaintenanceWindow{}, classify(ErrNotFound, fmt.Errorf("store: add maintenance window: check %q not found", w.Check))
	}
	if count >= MaxMaintenanceWindows {
		return config.MaintenanceWindow{}, classify(ErrValidation, fmt.Errorf("store: add maintenance window: a check can have at most %d windows", MaxMaintenanceWindows))
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO maintenance_windows (check_name, weekdays, start_minute, duration_ns, zone) VALUES (?, ?, ?, ?, ?)",
		w.Check, config.WeekdayMask(w.Weekdays), w.StartMinute, int64(w.Duration), w.Zone)
	if err != nil {
		return config.MaintenanceWindow{}, fmt.Errorf("store: add maintenance window: %w", err)
	}
	if w.ID, err = res.LastInsertId(); err != nil {
		return config.MaintenanceWindow{}, fmt.Errorf("store: add maintenance window: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return config.MaintenanceWindow{}, fmt.Errorf("store: add maintenance window: %w", err)
	}
	return w, nil
}

// UpdateMaintenanceWindow replaces the window with w.ID, which must belong to
// w.Check (otherwise ErrNotFound). Its check cannot change.
func (s *Store) UpdateMaintenanceWindow(ctx context.Context, w config.MaintenanceWindow) error {
	if err := config.ValidateMaintenanceWindow(w); err != nil {
		return classify(ErrValidation, fmt.Errorf("store: update maintenance window: %w", err))
	}
	w = canonicalWindow(w)
	res, err := s.db.ExecContext(ctx, "UPDATE maintenance_windows SET weekdays = ?, start_minute = ?, duration_ns = ?, zone = ? WHERE id = ? AND check_name = ?",
		config.WeekdayMask(w.Weekdays), w.StartMinute, int64(w.Duration), w.Zone, w.ID, w.Check)
	if err != nil {
		return fmt.Errorf("store: update maintenance window: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return fmt.Errorf("store: update maintenance window: %w", err)
		}
		return classify(ErrNotFound, fmt.Errorf("store: update maintenance window: window %d of check %q not found", w.ID, w.Check))
	}
	return nil
}

// DeleteMaintenanceWindow removes window id of a check; a window of another
// check or an unknown ID is ErrNotFound.
func (s *Store) DeleteMaintenanceWindow(ctx context.Context, check string, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM maintenance_windows WHERE id = ? AND check_name = ?", id, check)
	if err != nil {
		return fmt.Errorf("store: delete maintenance window: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return fmt.Errorf("store: delete maintenance window: %w", err)
		}
		return classify(ErrNotFound, fmt.Errorf("store: delete maintenance window: window %d of check %q not found", id, check))
	}
	return nil
}

// MaintenanceWindows returns a check's windows in creation order.
func (s *Store) MaintenanceWindows(ctx context.Context, check string) ([]config.MaintenanceWindow, error) {
	return s.windows(ctx, "SELECT "+windowColumns+" FROM maintenance_windows WHERE check_name = ? ORDER BY id", check)
}

// AllMaintenanceWindows returns every window ordered by ID.
func (s *Store) AllMaintenanceWindows(ctx context.Context) ([]config.MaintenanceWindow, error) {
	return s.windows(ctx, "SELECT "+windowColumns+" FROM maintenance_windows ORDER BY id")
}

func (s *Store) windows(ctx context.Context, query string, args ...any) ([]config.MaintenanceWindow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: maintenance windows: %w", err)
	}
	defer rows.Close()
	var out []config.MaintenanceWindow
	for rows.Next() {
		w, err := scanWindow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: maintenance windows: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: maintenance windows: %w", err)
	}
	return out, nil
}
