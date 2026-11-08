package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/heohlee/up/internal/check"
	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
)

// pingTokenPrefix marks heartbeat ping tokens, so secret scanners can tell
// them from the API access token ("up_").
const pingTokenPrefix = "uphb_"

func pingTokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// setPingToken gives the heartbeat called name a new random token (256 bits,
// base64url) sealed to the daemon's public key, with the hash pings are
// matched by, and returns it. The previous token stops matching at once.
func setPingToken(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	var publicKey []byte
	err := tx.QueryRowContext(ctx, "SELECT public_key FROM secret_key WHERE id = 1").Scan(&publicKey)
	if errors.Is(err, sql.ErrNoRows) {
		return "", classify(ErrValidation, errors.New("heartbeat monitors need the encryption key: start `up serve` once"))
	}
	if err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate ping token: %w", err)
	}
	token := pingTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	sealed, err := secret.Seal(publicKey, token)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, "UPDATE checks SET ping_token = ?, ping_token_key = ?, ping_token_hash = ? WHERE name = ?",
		sealed.Data, sealed.Key, pingTokenHash(token), name)
	return token, err
}

// keepPingToken is the token rule of an update: a heartbeat without a token
// gets one, any other kind has none.
func keepPingToken(ctx context.Context, tx *sql.Tx, c config.Check) error {
	if c.Kind != config.CheckHeartbeat {
		_, err := tx.ExecContext(ctx, "UPDATE checks SET ping_token = x'', ping_token_key = x'', ping_token_hash = x'' WHERE name = ?", c.Name)
		return err
	}
	var has bool
	if err := tx.QueryRowContext(ctx, "SELECT length(ping_token_hash) > 0 FROM checks WHERE name = ?", c.Name).Scan(&has); err != nil {
		return err
	}
	if has {
		return nil
	}
	_, err := setPingToken(ctx, tx, c.Name)
	return err
}

// HeartbeatToken returns the sealed ping token of the heartbeat monitor
// called name, which only the daemon's private key opens. A missing monitor
// or one of another kind is ErrNotFound.
func (s *Store) HeartbeatToken(ctx context.Context, name string) (secret.Sealed, error) {
	var sealed secret.Sealed
	err := s.db.QueryRowContext(ctx, "SELECT ping_token, ping_token_key FROM checks WHERE name = ? AND kind = ?", name, config.CheckHeartbeat).
		Scan(&sealed.Data, &sealed.Key)
	if errors.Is(err, sql.ErrNoRows) {
		return secret.Sealed{}, classify(ErrNotFound, fmt.Errorf("store: heartbeat token: heartbeat monitor %q not found", name))
	}
	if err != nil {
		return secret.Sealed{}, fmt.Errorf("store: heartbeat token: %w", err)
	}
	return sealed, nil
}

// RegenerateHeartbeatToken replaces the ping token of the heartbeat monitor
// called name and returns the new one; the old one stops working at once.
// A missing monitor or one of another kind is ErrNotFound.
func (s *Store) RegenerateHeartbeatToken(ctx context.Context, name string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("store: regenerate heartbeat token: %w", err)
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM checks WHERE name = ? AND kind = ?)", name, config.CheckHeartbeat).Scan(&exists); err != nil {
		return "", fmt.Errorf("store: regenerate heartbeat token: %w", err)
	}
	if !exists {
		return "", classify(ErrNotFound, fmt.Errorf("store: regenerate heartbeat token: heartbeat monitor %q not found", name))
	}
	token, err := setPingToken(ctx, tx, name)
	if err != nil {
		return "", fmt.Errorf("store: regenerate heartbeat token: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("store: regenerate heartbeat token: %w", err)
	}
	return token, nil
}

// queryRower is a *sql.DB or a *sql.Tx.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// heartbeatByToken returns the name of the heartbeat monitor whose token is
// token, or "" when none matches. The monitor is found by the token's hash,
// never by opening stored tokens, and the hashes are compared in constant
// time.
func heartbeatByToken(ctx context.Context, q queryRower, token string) (string, error) {
	hash := pingTokenHash(token)
	var name string
	var stored []byte
	err := q.QueryRowContext(ctx, "SELECT name, ping_token_hash FROM checks WHERE ping_token_hash = ? AND kind = ?", hash, config.CheckHeartbeat).Scan(&name, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if subtle.ConstantTimeCompare(stored, hash) != 1 {
		return "", nil
	}
	return name, nil
}

// HeartbeatTokenKnown reports whether token is the ping token of a heartbeat
// monitor, without recording anything. A former token is not known.
func (s *Store) HeartbeatTokenKnown(ctx context.Context, token string) (bool, error) {
	name, err := heartbeatByToken(ctx, s.db, token)
	if err != nil {
		return false, fmt.Errorf("store: heartbeat token lookup: %w", err)
	}
	return name != "", nil
}

// PingHeartbeat records a ping (or, with failed, a reported failure) for the
// heartbeat monitor whose token is token, and reports whether one matched.
// Any other text, including a former token, matches nothing and records
// nothing. The result is stamped at at, or just after the monitor's newest
// result when that is not earlier: a request that began before a deadline
// but is stored after the miss was recorded is still the newest result, so
// the monitor observes it and recovers.
func (s *Store) PingHeartbeat(ctx context.Context, token string, failed bool, at time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("store: heartbeat ping: %w", err)
	}
	defer tx.Rollback()
	name, err := heartbeatByToken(ctx, tx, token)
	if err != nil {
		return false, fmt.Errorf("store: heartbeat ping: %w", err)
	}
	if name == "" {
		return false, nil
	}
	var newest sql.NullInt64
	if err := tx.QueryRowContext(ctx, "SELECT MAX(at_ns) FROM results WHERE check_name = ?", name).Scan(&newest); err != nil {
		return false, fmt.Errorf("store: heartbeat ping: %w", err)
	}
	if newest.Valid && at.UnixNano() <= newest.Int64 {
		at = time.Unix(0, newest.Int64+1).In(at.Location())
	}
	r := check.Ping(name, at)
	if failed {
		r = check.Fail(name, at)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error) VALUES (?, ?, ?, 0, 0, ?)",
		r.CheckName, r.At.UnixNano(), string(r.Status), r.Error); err != nil {
		return false, fmt.Errorf("store: heartbeat ping: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: heartbeat ping: %w", err)
	}
	return true, nil
}

// RecordMissed stores a heartbeat's missed-deadline result r only while the
// heartbeat c still exists with the same period and grace and has no result
// after newest, the result the deadline was computed from. A ping that
// arrived meanwhile wins: the miss is not stored. The check and the insert
// are one statement.
func (s *Store) RecordMissed(ctx context.Context, c config.Check, newest time.Time, r check.Result) (bool, error) {
	if r.CheckName != c.Name || r.Status != check.StatusDown {
		return false, fmt.Errorf("store: refusing to record a missed deadline %+v for %s", r, c.Name)
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO results (check_name, at_ns, status, latency_ns, status_code, error)
SELECT ?1, ?2, 'down', 0, 0, ?3
 WHERE EXISTS (SELECT 1 FROM checks WHERE name = ?1 AND kind = ?4 AND period_ns = ?5 AND grace_ns = ?6)
   AND NOT EXISTS (SELECT 1 FROM results WHERE check_name = ?1 AND at_ns > ?7)`,
		c.Name, r.At.UnixNano(), r.Error, config.CheckHeartbeat, int64(c.Period), int64(c.Grace), newest.UnixNano())
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", c.Name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: record %s: %w", c.Name, err)
	}
	return n > 0, nil
}

// SetAPIListener records the API the running daemon serves; the zero value
// records that it has none.
func (s *Store) SetAPIListener(ctx context.Context, a config.APIListener) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO daemon_api (id, listen, tls) VALUES (1, ?, ?) ON CONFLICT (id) DO UPDATE SET listen = excluded.listen, tls = excluded.tls",
		a.Address, a.TLS)
	if err != nil {
		return fmt.Errorf("store: set api listener: %w", err)
	}
	return nil
}

// APIListener returns the API the daemon last started with; the zero value
// when it had none or never ran.
func (s *Store) APIListener(ctx context.Context) (config.APIListener, error) {
	var a config.APIListener
	err := s.db.QueryRowContext(ctx, "SELECT listen, tls FROM daemon_api WHERE id = 1").Scan(&a.Address, &a.TLS)
	if errors.Is(err, sql.ErrNoRows) {
		return config.APIListener{}, nil
	}
	if err != nil {
		return config.APIListener{}, fmt.Errorf("store: api listener: %w", err)
	}
	return a, nil
}
