package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/giulianotesta7/up/internal/config"
)

// Channel test request limits.
const (
	// MaxChannelTestError is the longest stored error text in runes.
	MaxChannelTestError = 200
	// MaxChannelTestHistory is how many requests are kept per channel; older
	// finished ones are deleted when a new one is enqueued.
	MaxChannelTestHistory = 20
)

const testColumns = "id, channel, status, error, requested_ns, started_ns, finished_ns"

func scanTest(row scanner) (config.ChannelTest, error) {
	var t config.ChannelTest
	var status string
	var requested, started, finished int64
	if err := row.Scan(&t.ID, &t.Channel, &status, &t.Error, &requested, &started, &finished); err != nil {
		return config.ChannelTest{}, err
	}
	t.Status, t.Requested = config.ChannelTestStatus(status), fromNanos(requested)
	if started != 0 {
		t.Started = fromNanos(started)
	}
	if finished != 0 {
		t.Finished = fromNanos(finished)
	}
	return t, nil
}

// EnqueueChannelTest records a pending test request for a channel at at and
// returns it. An unknown channel is ErrNotFound; a channel that already has a
// pending or sending request is ErrExists (a database constraint, so it holds
// across processes). It does not send anything: the daemon claims requests.
func (s *Store) EnqueueChannelTest(ctx context.Context, channel string, at time.Time) (config.ChannelTest, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return config.ChannelTest{}, fmt.Errorf("store: enqueue channel test: %w", err)
	}
	defer tx.Rollback()
	var exists, active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM channels WHERE name = ?),
EXISTS (SELECT 1 FROM channel_tests WHERE channel = ? AND status IN ('pending', 'sending'))`, channel, channel).Scan(&exists, &active); err != nil {
		return config.ChannelTest{}, fmt.Errorf("store: enqueue channel test: %w", err)
	}
	if !exists {
		return config.ChannelTest{}, classify(ErrNotFound, fmt.Errorf("store: enqueue channel test: channel %q not found", channel))
	}
	if active {
		return config.ChannelTest{}, classify(ErrExists, fmt.Errorf("store: enqueue channel test: a test of channel %q is already in progress", channel))
	}
	res, err := tx.ExecContext(ctx, "INSERT INTO channel_tests (channel, status, error, requested_ns, started_ns, finished_ns) VALUES (?, 'pending', '', ?, 0, 0)", channel, at.UnixNano())
	if err != nil {
		return config.ChannelTest{}, fmt.Errorf("store: enqueue channel test: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return config.ChannelTest{}, fmt.Errorf("store: enqueue channel test: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_tests WHERE channel = ? AND status IN ('ok', 'error') AND id NOT IN
(SELECT id FROM channel_tests WHERE channel = ? ORDER BY id DESC LIMIT ?)`, channel, channel, MaxChannelTestHistory); err != nil {
		return config.ChannelTest{}, fmt.Errorf("store: enqueue channel test: %w", err)
	}
	t, err := scanTest(tx.QueryRowContext(ctx, "SELECT "+testColumns+" FROM channel_tests WHERE id = ?", id))
	if err != nil {
		return config.ChannelTest{}, fmt.Errorf("store: enqueue channel test: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return config.ChannelTest{}, fmt.Errorf("store: enqueue channel test: %w", err)
	}
	return t, nil
}

// ClaimChannelTest moves the oldest pending request to sending at at and
// returns it; ok is false when none is pending. The daemon calls it, then
// sends, then calls CompleteChannelTest.
func (s *Store) ClaimChannelTest(ctx context.Context, at time.Time) (t config.ChannelTest, ok bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return config.ChannelTest{}, false, fmt.Errorf("store: claim channel test: %w", err)
	}
	defer tx.Rollback()
	t, err = scanTest(tx.QueryRowContext(ctx, "SELECT "+testColumns+" FROM channel_tests WHERE status = 'pending' ORDER BY id LIMIT 1"))
	if errors.Is(err, sql.ErrNoRows) {
		return config.ChannelTest{}, false, nil
	}
	if err != nil {
		return config.ChannelTest{}, false, fmt.Errorf("store: claim channel test: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE channel_tests SET status = 'sending', started_ns = ? WHERE id = ?", at.UnixNano(), t.ID); err != nil {
		return config.ChannelTest{}, false, fmt.Errorf("store: claim channel test: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return config.ChannelTest{}, false, fmt.Errorf("store: claim channel test: %w", err)
	}
	t.Status, t.Started = config.TestSending, fromNanos(at.UnixNano())
	return t, true, nil
}

// CompleteChannelTest finishes a sending request at at: ok when errText is
// empty, otherwise error with errText made safe (see SafeTestError). A request
// that is not sending, including an unknown ID, is ErrNotFound.
func (s *Store) CompleteChannelTest(ctx context.Context, id int64, errText string, at time.Time) error {
	status, text := config.TestOK, ""
	if errText != "" {
		status, text = config.TestError, SafeTestError(errText)
	}
	res, err := s.db.ExecContext(ctx, "UPDATE channel_tests SET status = ?, error = ?, finished_ns = ? WHERE id = ? AND status = 'sending'", string(status), text, at.UnixNano(), id)
	if err != nil {
		return fmt.Errorf("store: complete channel test: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return fmt.Errorf("store: complete channel test: %w", err)
		}
		return classify(ErrNotFound, fmt.Errorf("store: complete channel test: no sending test with id %d", id))
	}
	return nil
}

// FailSendingChannelTests marks every sending request as failed with reason,
// returning how many; the daemon calls it at start so a crash mid-send cannot
// block a channel forever. Pending requests are left for the daemon to claim.
func (s *Store) FailSendingChannelTests(ctx context.Context, reason string, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "UPDATE channel_tests SET status = 'error', error = ?, finished_ns = ? WHERE status = 'sending'", SafeTestError(reason), at.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("store: fail sending channel tests: %w", err)
	}
	return res.RowsAffected()
}

// LatestChannelTest returns a channel's newest request; ok is false when it
// never had one.
func (s *Store) LatestChannelTest(ctx context.Context, channel string) (config.ChannelTest, bool, error) {
	t, err := scanTest(s.db.QueryRowContext(ctx, "SELECT "+testColumns+" FROM channel_tests WHERE channel = ? ORDER BY id DESC LIMIT 1", channel))
	if errors.Is(err, sql.ErrNoRows) {
		return config.ChannelTest{}, false, nil
	}
	if err != nil {
		return config.ChannelTest{}, false, fmt.Errorf("store: latest channel test: %w", err)
	}
	return t, true, nil
}

// LatestChannelTests returns each channel's newest request, ordered by channel.
func (s *Store) LatestChannelTests(ctx context.Context) ([]config.ChannelTest, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+testColumns+" FROM channel_tests WHERE id IN (SELECT MAX(id) FROM channel_tests GROUP BY channel) ORDER BY channel")
	if err != nil {
		return nil, fmt.Errorf("store: latest channel tests: %w", err)
	}
	defer rows.Close()
	var out []config.ChannelTest
	for rows.Next() {
		t, err := scanTest(rows)
		if err != nil {
			return nil, fmt.Errorf("store: latest channel tests: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: latest channel tests: %w", err)
	}
	return out, nil
}

// CountChannelTests returns how many requests a channel has stored.
func (s *Store) CountChannelTests(ctx context.Context, channel string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM channel_tests WHERE channel = ?", channel).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count channel tests: %w", err)
	}
	return n, nil
}

// SafeTestError makes error text fit to store and show: control characters
// become spaces, whitespace collapses, and it is cut to MaxChannelTestError
// runes. Text with nothing printable becomes "test failed". Callers must
// still keep secrets (URLs, credentials) out of it.
func SafeTestError(text string) string {
	text = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)), " ")
	if text == "" {
		return "test failed"
	}
	if r := []rune(text); len(r) > MaxChannelTestError {
		text = string(r[:MaxChannelTestError])
	}
	return text
}
