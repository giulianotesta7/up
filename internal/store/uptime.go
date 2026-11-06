package store

import (
	"context"
	"fmt"
	"time"
)

// Uptime measures time spent up and observed in [since, until] like
// UptimeWindow, without counting incidents. It reads the window's results
// once, in order, and adds the spans in Go: SQLite window functions cost
// several times more per row, which adds up over a 30-day window on every
// refresh.
func (s *Store) Uptime(ctx context.Context, name string, since, until time.Time) (up, observed time.Duration, err error) {
	if !until.After(since) {
		return 0, 0, nil
	}
	sinceNS, untilNS := since.UnixNano(), until.UnixNano()
	// The range starts at the seed's timestamp so it is one index range,
	// already in order. Rows sharing that timestamp before the seed end at
	// or before since and add nothing.
	rows, err := s.db.QueryContext(ctx, `
SELECT at_ns, status = 'up' FROM results
 WHERE check_name = ?1 AND at_ns <= ?3 AND at_ns >= COALESCE(
  (SELECT at_ns FROM results WHERE check_name = ?1 AND at_ns < ?2 ORDER BY at_ns DESC, id DESC LIMIT 1), ?2)
 ORDER BY at_ns, id`, name, sinceNS, untilNS)
	if err != nil {
		return 0, 0, fmt.Errorf("store: uptime %s: %w", name, err)
	}
	defer rows.Close()
	var upNS, observedNS, atNS int64
	var isUp, seen bool
	// A result holds until its successor, or until; only the part from
	// since counts.
	span := func(endNS int64) {
		if !seen {
			return
		}
		d := max(0, endNS-max(atNS, sinceNS))
		observedNS += d
		if isUp {
			upNS += d
		}
	}
	for rows.Next() {
		var next int64
		var nextUp bool
		if err := rows.Scan(&next, &nextUp); err != nil {
			return 0, 0, fmt.Errorf("store: uptime %s: %w", name, err)
		}
		span(next)
		atNS, isUp, seen = next, nextUp, true
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("store: uptime %s: %w", name, err)
	}
	span(untilNS)
	return time.Duration(upNS), time.Duration(observedNS), nil
}

// UptimeWindow measures time spent up and observed in [since, until]. A result
// holds until its successor; the last result before since seeds the window.
// Incidents use Outages' definition and count outages intersecting the window
// (an outage ending exactly at since is no longer active). Up and observed
// come from Uptime.
func (s *Store) UptimeWindow(ctx context.Context, name string, since, until time.Time) (up, observed time.Duration, incidents int, err error) {
	if !until.After(since) {
		return 0, 0, 0, nil
	}
	if up, observed, err = s.Uptime(ctx, name, since, until); err != nil {
		return 0, 0, 0, err
	}
	// Outages closes a start at the first up with a strictly later timestamp,
	// not necessarily the next row. Starts sharing the last up timestamp
	// before since therefore still overlap, even if an up follows at that
	// same timestamp. Only that timestamp bucket and the window need scans.
	err = s.db.QueryRowContext(ctx, `
WITH last_up AS (
 SELECT at_ns FROM results WHERE check_name = ?1 AND status = 'up' AND at_ns <= ?2
 ORDER BY at_ns DESC, id DESC LIMIT 1
), seed AS (
 SELECT id, at_ns, status FROM results WHERE check_name = ?1 AND at_ns < ?2
 ORDER BY at_ns DESC, id DESC LIMIT 1
), bucket_seed AS (
 SELECT id, at_ns, status FROM results
 WHERE check_name = ?1 AND at_ns < (SELECT at_ns FROM last_up)
 ORDER BY at_ns DESC, id DESC LIMIT 1
), bucket AS (
 SELECT * FROM bucket_seed
 UNION ALL
 SELECT id, at_ns, status FROM results
 WHERE check_name = ?1 AND at_ns = (SELECT at_ns FROM last_up) AND at_ns < ?2
), bucket_transitions AS (
 SELECT at_ns, status, LAG(status) OVER (ORDER BY at_ns, id) AS prev FROM bucket
), window_rows AS (
 SELECT * FROM seed
 UNION ALL
 SELECT id, at_ns, status FROM results WHERE check_name = ?1 AND at_ns >= ?2 AND at_ns <= ?3
), window_transitions AS (
 SELECT at_ns, status, LAG(status) OVER (ORDER BY at_ns, id) AS prev FROM window_rows
)
SELECT
 (SELECT COUNT(*) FROM window_transitions WHERE at_ns >= ?2 AND status = 'down' AND (prev IS NULL OR prev = 'up'))
 + (SELECT COUNT(*) FROM bucket_transitions WHERE at_ns = (SELECT at_ns FROM last_up) AND status = 'down' AND (prev IS NULL OR prev = 'up'))
 + CASE WHEN (SELECT status FROM seed) = 'down' AND (
     NOT EXISTS (SELECT 1 FROM last_up) OR (
       (SELECT at_ns FROM last_up) < ?2 AND (SELECT at_ns FROM seed) > (SELECT at_ns FROM last_up)
       AND (SELECT status FROM results WHERE check_name = ?1 AND at_ns = (SELECT at_ns FROM last_up) ORDER BY id DESC LIMIT 1) = 'up'
     )) THEN 1 ELSE 0 END`, name, since.UnixNano(), until.UnixNano()).Scan(&incidents)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("store: uptime incidents %s: %w", name, err)
	}
	return up, observed, incidents, nil
}
