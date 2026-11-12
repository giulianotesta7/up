package store

import (
	"context"
	"fmt"
	"time"

	"github.com/heohlee/up/internal/config"
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
		return classify(ErrNotFound, fmt.Errorf("store: set mute: name %q not found", name))
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
// notAfter, so a mute extended meanwhile survives. Mutes of the check's tags
// that ended by then are removed too: they hold nothing for any monitor.
func (s *Store) ClearMute(ctx context.Context, name string, notAfter time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: clear mute: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM mutes WHERE check_name = ? AND until_ns <= ?", name, notAfter.UnixNano()); err != nil {
		return fmt.Errorf("store: clear mute: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM tag_mutes WHERE until_ns <= ?2 AND EXISTS (SELECT 1 FROM checks c WHERE c.name = ?1 AND "+carriesTag("c", "tag_mutes.tag")+")", name, notAfter.UnixNano()); err != nil {
		return fmt.Errorf("store: clear mute: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: clear mute: %w", err)
	}
	return nil
}

// muteRows selects every mute that applies to a check as (check, until,
// was_alerting, tag): its own, and those of the tags it carries with the
// recipients' known state (false when none is recorded). The rows of one
// check are ordered by end, a tag's mute before the own one ending at the
// same time, so the last one is the check's mute and the own mute wins a tie.
const muteRows = `
SELECT check_name, until_ns, was_alerting, '' FROM mutes WHERE check_name = ?1 OR ?1 IS NULL
UNION ALL
SELECT c.name, t.until_ns, COALESCE((SELECT known_alerting FROM alert_state a WHERE a.check_name = c.name), 0), t.tag
  FROM tag_mutes t JOIN checks c ON ` + "instr(',' || c.tags || ',', ',' || t.tag || ',') > 0" + `
 WHERE c.name = ?1 OR ?1 IS NULL
ORDER BY 1, 2, 4 DESC`

// readMutes returns the mute of each check (all when name is nil) ordered
// by check name: the one that ends last of its own and its tags' (see
// config.Mute). Against a tag's, the own mute keeps its notification state.
func (s *Store) readMutes(ctx context.Context, name any) ([]config.Mute, error) {
	rows, err := s.db.QueryContext(ctx, muteRows, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []config.Mute
	own := map[string]bool{}
	for rows.Next() {
		var m config.Mute
		var ns int64
		if err := rows.Scan(&m.Check, &ns, &m.WasAlerting, &m.Tag); err != nil {
			return nil, err
		}
		m.Until = fromNanos(ns)
		if m.Tag == "" {
			own[m.Check] = m.WasAlerting
		} else if was, ok := own[m.Check]; ok {
			m.WasAlerting = was
		}
		if n := len(result); n > 0 && result[n-1].Check == m.Check {
			result[n-1] = m
		} else {
			result = append(result, m)
		}
	}
	return result, rows.Err()
}

// Mute returns a check's mute, if present: the one that ends last of its own
// and its tags' (Tag names the tag when that is a tag's). It may be over.
func (s *Store) Mute(ctx context.Context, name string) (config.Mute, bool, error) {
	mutes, err := s.readMutes(ctx, name)
	if err != nil {
		return config.Mute{}, false, fmt.Errorf("store: mute: %w", err)
	}
	if len(mutes) == 0 {
		return config.Mute{}, false, nil
	}
	return mutes[0], true, nil
}

// Mutes returns each check's mute (see Mute) ordered by check name.
func (s *Store) Mutes(ctx context.Context) ([]config.Mute, error) {
	mutes, err := s.readMutes(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("store: mutes: %w", err)
	}
	return mutes, nil
}
