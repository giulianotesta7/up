package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Event is the alert an attempt was about.
type Event string

// The alerts that are recorded. Channel tests are not.
const (
	EventDown      Event = "monitor.down"
	EventRecovered Event = "monitor.recovered"
	EventCert      Event = "cert.expiring"
)

// Outcome is what became of an alert for one channel: handed over (sent),
// refused or lost on the way (failed), or kept back by a mute or a
// maintenance window (held).
type Outcome string

// The outcomes an attempt can have.
const (
	OutcomeSent   Outcome = "sent"
	OutcomeFailed Outcome = "failed"
	OutcomeHeld   Outcome = "held"
)

// Attempt limits.
const (
	// MaxAttemptReason is the longest stored reason in runes.
	MaxAttemptReason = 200
	// DefaultAttemptLimit applies when AttemptFilter.Limit is not positive.
	DefaultAttemptLimit = 50
	// MaxAttemptLimit caps AttemptFilter.Limit.
	MaxAttemptLimit = 500
)

// Attempt is one alert delivery attempt of a monitor through a channel.
// Reason says why it failed or was held; callers keep secrets out of it.
type Attempt struct {
	At      time.Time
	Monitor string
	Channel string
	Event   Event
	Outcome Outcome
	Reason  string
}

// AttemptFilter selects attempts. Empty Monitor or Channel match any; a
// nonzero Since keeps attempts at or after it.
type AttemptFilter struct {
	Monitor string
	Channel string
	Since   time.Time
	Limit   int
}

// FoldSpace turns control characters into spaces and folds every run of
// white space into one space, trimmed, the way a stored reason is written.
// Whoever scrubs a reason of secrets scrubs its folded form.
func FoldSpace(text string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)), " ")
}

func cleanReason(text string) string {
	text = FoldSpace(text)
	if r := []rune(text); len(r) > MaxAttemptReason {
		text = string(r[:MaxAttemptReason])
	}
	return text
}

// RecordAttempt stores one attempt. An unknown event or outcome, or an empty
// monitor or channel, is ErrValidation; a monitor that no longer exists (it
// was deleted while its alert was in flight) is ErrNotFound. Either way
// nothing is stored. The channel is not checked: deleting a channel keeps its
// history.
func (s *Store) RecordAttempt(ctx context.Context, a Attempt) error {
	switch a.Event {
	case EventDown, EventRecovered, EventCert:
	default:
		return classify(ErrValidation, fmt.Errorf("store: record attempt: unknown event %q", a.Event))
	}
	switch a.Outcome {
	case OutcomeSent, OutcomeFailed, OutcomeHeld:
	default:
		return classify(ErrValidation, fmt.Errorf("store: record attempt: unknown outcome %q", a.Outcome))
	}
	if a.Monitor == "" || a.Channel == "" {
		return classify(ErrValidation, fmt.Errorf("store: record attempt: monitor and channel are required"))
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO delivery_attempts (at_ns, monitor, channel, event, outcome, reason)
SELECT ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM checks WHERE name = ?)`,
		a.At.UnixNano(), a.Monitor, a.Channel, string(a.Event), string(a.Outcome), cleanReason(a.Reason), a.Monitor)
	if err != nil {
		return fmt.Errorf("store: record attempt: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: record attempt: %w", err)
	} else if n == 0 {
		return classify(ErrNotFound, fmt.Errorf("store: record attempt: monitor %q not found", a.Monitor))
	}
	return nil
}

const attemptColumns = "at_ns, monitor, channel, event, outcome, reason"

type attemptRow interface{ Scan(...any) error }

func scanAttempt(row attemptRow) (Attempt, error) {
	var a Attempt
	var at int64
	var event, outcome string
	if err := row.Scan(&at, &a.Monitor, &a.Channel, &event, &outcome, &a.Reason); err != nil {
		return Attempt{}, err
	}
	a.At, a.Event, a.Outcome = fromNanos(at), Event(event), Outcome(outcome)
	return a, nil
}

// Attempts returns the attempts matching f, newest first, at most f.Limit
// (DefaultAttemptLimit when not positive, MaxAttemptLimit at most).
func (s *Store) Attempts(ctx context.Context, f AttemptFilter) ([]Attempt, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultAttemptLimit
	}
	limit = min(limit, MaxAttemptLimit)
	// Only the filters that are set go into the query, so SQLite can use the
	// monitor or channel index instead of scanning every attempt by time.
	where, args := []string{"1"}, []any{}
	if f.Monitor != "" {
		where, args = append(where, "monitor = ?"), append(args, f.Monitor)
	}
	if f.Channel != "" {
		where, args = append(where, "channel = ?"), append(args, f.Channel)
	}
	if !f.Since.IsZero() {
		where, args = append(where, "at_ns >= ?"), append(args, f.Since.UnixNano())
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT `+attemptColumns+` FROM delivery_attempts
 WHERE `+strings.Join(where, " AND ")+`
 ORDER BY at_ns DESC, id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("store: attempts: %w", err)
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("store: attempts: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: attempts: %w", err)
	}
	return out, nil
}

// LatestAttempt returns the newest attempt of monitor through channel for any
// of events, and false when there is none.
func (s *Store) LatestAttempt(ctx context.Context, monitor, channel string, events ...Event) (Attempt, bool, error) {
	args := []any{monitor, channel}
	marks := make([]string, len(events))
	for i, e := range events {
		marks[i], args = "?", append(args, string(e))
	}
	a, err := scanAttempt(s.db.QueryRowContext(ctx, `
SELECT `+attemptColumns+` FROM delivery_attempts
 WHERE monitor = ? AND channel = ? AND event IN (`+strings.Join(marks, ", ")+`)
 ORDER BY at_ns DESC, id DESC LIMIT 1`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, false, nil
	}
	if err != nil {
		return Attempt{}, false, fmt.Errorf("store: latest attempt: %w", err)
	}
	return a, true, nil
}

// LastAttempts returns the newest attempt of each channel that has one,
// ordered by channel.
func (s *Store) LastAttempts(ctx context.Context) ([]Attempt, error) {
	// Walk the channel index one channel at a time and take each one's
	// newest attempt from it, instead of numbering every attempt.
	rows, err := s.db.QueryContext(ctx, `
WITH RECURSIVE channels(name) AS (
  SELECT MIN(channel) FROM delivery_attempts
  UNION ALL
  SELECT (SELECT MIN(channel) FROM delivery_attempts WHERE channel > name) FROM channels WHERE name IS NOT NULL)
SELECT `+attemptColumns+` FROM channels JOIN delivery_attempts ON id = (
  SELECT id FROM delivery_attempts WHERE channel = channels.name ORDER BY at_ns DESC, id DESC LIMIT 1)
 ORDER BY channel`)
	if err != nil {
		return nil, fmt.Errorf("store: last attempts: %w", err)
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("store: last attempts: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: last attempts: %w", err)
	}
	return out, nil
}
