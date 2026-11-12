package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/heohlee/up/internal/config"
)

// MaxMaintenanceWindows is how many windows one check, or one tag, may have.
const MaxMaintenanceWindows = 20

// windowColumns are read by scanWindow. A tag's window is stored with an
// empty check_name.
const windowColumns = "id, check_name, weekdays, start_minute, duration_ns, zone, tag"

func scanWindow(row scanner) (config.MaintenanceWindow, error) {
	var w config.MaintenanceWindow
	var mask int
	var duration int64
	if err := row.Scan(&w.ID, &w.Check, &mask, &w.StartMinute, &duration, &w.Zone, &w.Tag); err != nil {
		return config.MaintenanceWindow{}, err
	}
	w.Weekdays, w.Duration = config.WeekdaysFromMask(mask), time.Duration(duration)
	return w, nil
}

func canonicalWindow(w config.MaintenanceWindow) config.MaintenanceWindow {
	w.Weekdays = slices.Clone(w.Weekdays)
	slices.Sort(w.Weekdays)
	w.Tag = strings.ToLower(strings.TrimSpace(w.Tag))
	return w
}

// AddMaintenanceWindow stores a validated window (at most MaxMaintenanceWindows
// per check or tag) and returns it with its ID and weekdays sorted. A window
// with a Tag belongs to that tag (normalized) and its Check is ignored. An
// unknown check or a tag no monitor carries is ErrNotFound; an invalid window
// or a full check or tag is ErrValidation.
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
	owner, target, what := w.Check, "", "check"
	query := "SELECT EXISTS (SELECT 1 FROM checks WHERE name = ?1), (SELECT COUNT(*) FROM maintenance_windows WHERE check_name = ?1)"
	if w.Tag != "" {
		owner, target, what = "", w.Tag, "tag"
		query = "SELECT EXISTS (SELECT 1 FROM checks c WHERE " + carriesTag("c", "?1") + "), (SELECT COUNT(*) FROM maintenance_windows WHERE check_name = '' AND tag = ?1)"
	}
	if err := tx.QueryRowContext(ctx, query, cmp.Or(target, owner)).Scan(&exists, &count); err != nil {
		return config.MaintenanceWindow{}, fmt.Errorf("store: add maintenance window: %w", err)
	}
	if !exists {
		return config.MaintenanceWindow{}, classify(ErrNotFound, fmt.Errorf("store: add maintenance window: %s %q not found", what, cmp.Or(target, owner)))
	}
	if count >= MaxMaintenanceWindows {
		return config.MaintenanceWindow{}, classify(ErrValidation, fmt.Errorf("store: add maintenance window: a %s can have at most %d windows", what, MaxMaintenanceWindows))
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO maintenance_windows (check_name, weekdays, start_minute, duration_ns, zone, tag) VALUES (?, ?, ?, ?, ?, ?)",
		owner, config.WeekdayMask(w.Weekdays), w.StartMinute, int64(w.Duration), w.Zone, target)
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
// w.Check, or to w.Tag when set (otherwise ErrNotFound). Its check or tag
// cannot change.
func (s *Store) UpdateMaintenanceWindow(ctx context.Context, w config.MaintenanceWindow) error {
	if err := config.ValidateMaintenanceWindow(w); err != nil {
		return classify(ErrValidation, fmt.Errorf("store: update maintenance window: %w", err))
	}
	w = canonicalWindow(w)
	owner, what := w.Check, "check"
	if w.Tag != "" {
		owner, what = "", "tag"
	}
	res, err := s.db.ExecContext(ctx, "UPDATE maintenance_windows SET weekdays = ?, start_minute = ?, duration_ns = ?, zone = ? WHERE id = ? AND check_name = ? AND tag = ?",
		config.WeekdayMask(w.Weekdays), w.StartMinute, int64(w.Duration), w.Zone, w.ID, owner, w.Tag)
	if err != nil {
		return fmt.Errorf("store: update maintenance window: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return fmt.Errorf("store: update maintenance window: %w", err)
		}
		return classify(ErrNotFound, fmt.Errorf("store: update maintenance window: window %d of %s %q not found", w.ID, what, cmp.Or(w.Tag, w.Check)))
	}
	return nil
}

// DeleteMaintenanceWindow removes window id of a check, or of one of the
// check's tags (a member deletes the tag window it shows); any other window
// or an unknown ID is ErrNotFound.
func (s *Store) DeleteMaintenanceWindow(ctx context.Context, check string, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM maintenance_windows WHERE id = ?1 AND (check_name = ?2 OR (check_name = '' AND EXISTS (
SELECT 1 FROM checks c WHERE c.name = ?2 AND `+carriesTag("c", "maintenance_windows.tag")+`)))`, id, check)
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

// tagWindowsOf selects the windows of the tags of the checks row aliased c,
// reported for that check.
const tagWindowsOf = "SELECT w.id, c.name, w.weekdays, w.start_minute, w.duration_ns, w.zone, w.tag FROM maintenance_windows w JOIN checks c ON w.check_name = '' AND "

// MaintenanceWindows returns the windows that apply to a check in creation
// order: its own and those of the tags it carries now, which have Tag set
// and Check the check.
func (s *Store) MaintenanceWindows(ctx context.Context, check string) ([]config.MaintenanceWindow, error) {
	return s.windows(ctx, "SELECT "+windowColumns+" FROM maintenance_windows WHERE check_name = ?1 UNION ALL "+
		tagWindowsOf+carriesTag("c", "w.tag")+" WHERE c.name = ?1 ORDER BY 1", check)
}

// AllMaintenanceWindows returns every window ordered by ID: each check's own,
// and each tag's once for every check carrying it (as MaintenanceWindows
// reports it), in check name order for the same ID.
func (s *Store) AllMaintenanceWindows(ctx context.Context) ([]config.MaintenanceWindow, error) {
	return s.windows(ctx, "SELECT "+windowColumns+" FROM maintenance_windows WHERE check_name != '' UNION ALL "+
		tagWindowsOf+carriesTag("c", "w.tag")+" ORDER BY 1, 2")
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
