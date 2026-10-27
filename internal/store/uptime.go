package store

import (
	"context"
	"fmt"
	"time"
)

// UptimeWindow measures time spent up and observed in [since, until]. A result
// holds until its successor; the last result before since seeds the window.
// Incidents use Outages' definition and count outages intersecting the window
// (an outage ending exactly at since is no longer active). The uptime query
// uses an indexed seed lookup and bounded range.
func (s *Store) UptimeWindow(ctx context.Context, name string, since, until time.Time) (up, observed time.Duration, incidents int, err error) {
	if !until.After(since) {
		return 0, 0, 0, nil
	}
	var upNS, observedNS int64
	err = s.db.QueryRowContext(ctx, `
WITH seed AS (
 SELECT id, at_ns, status FROM results
 WHERE check_name = ?1 AND at_ns < ?2
 ORDER BY at_ns DESC, id DESC LIMIT 1
), window AS (
 SELECT * FROM seed
 UNION ALL
 SELECT id, at_ns, status FROM results
 WHERE check_name = ?1 AND at_ns >= ?2 AND at_ns <= ?3
), spans AS (
 SELECT at_ns, status,
        LEAD(at_ns, 1, ?3) OVER (ORDER BY at_ns, id) AS end_ns
 FROM window
)
SELECT COALESCE(SUM(CASE WHEN status = 'up' THEN MAX(0, end_ns - MAX(at_ns, ?2)) ELSE 0 END), 0),
       COALESCE(SUM(MAX(0, end_ns - MAX(at_ns, ?2))), 0)
FROM spans`, name, since.UnixNano(), until.UnixNano()).Scan(&upNS, &observedNS)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("store: uptime window %s: %w", name, err)
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
	return time.Duration(upNS), time.Duration(observedNS), incidents, nil
}
