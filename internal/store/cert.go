package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/giulianotesta7/up/internal/config"
)

func scanCert(row interface{ Scan(...any) error }) (config.Cert, error) {
	var c config.Cert
	var expiry, observed int64
	err := row.Scan(&c.Check, &c.Host, &c.Issuer, &c.Serial, &expiry, &observed, &c.Notified)
	if err == nil {
		c.NotAfter = fromNanos(expiry)
		c.Observed = fromNanos(observed)
	}
	return c, err
}

// Cert returns the latest certificate for a check, if any.
func (s *Store) Cert(ctx context.Context, name string) (config.Cert, bool, error) {
	c, err := scanCert(s.db.QueryRowContext(ctx, "SELECT check_name, host, issuer, serial, not_after_ns, observed_ns, notified FROM certs WHERE check_name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return config.Cert{}, false, nil
	}
	if err != nil {
		return config.Cert{}, false, fmt.Errorf("store: cert: %w", err)
	}
	return c, true, nil
}

// Certs returns certificates ordered by check name.
func (s *Store) Certs(ctx context.Context) ([]config.Cert, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT check_name, host, issuer, serial, not_after_ns, observed_ns, notified FROM certs ORDER BY check_name")
	if err != nil {
		return nil, fmt.Errorf("store: certs: %w", err)
	}
	defer rows.Close()
	var certs []config.Cert
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, fmt.Errorf("store: certs: %w", err)
		}
		certs = append(certs, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: certs: %w", err)
	}
	return certs, nil
}

// SaveCert replaces a check's observed certificate.
func (s *Store) SaveCert(ctx context.Context, c config.Cert) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO certs (check_name, host, issuer, serial, not_after_ns, observed_ns, notified) SELECT ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM checks WHERE name = ?) ON CONFLICT(check_name) DO UPDATE SET host=excluded.host, issuer=excluded.issuer, serial=excluded.serial, not_after_ns=excluded.not_after_ns, observed_ns=excluded.observed_ns, notified=excluded.notified`, c.Check, c.Host, c.Issuer, c.Serial, c.NotAfter.UnixNano(), c.Observed.UnixNano(), c.Notified, c.Check)
	if err != nil {
		return fmt.Errorf("store: save cert: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: save cert: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: save cert: name %q not found", c.Check)
	}
	return nil
}

// DeleteCert forgets a check's certificate; a missing one is not an error.
func (s *Store) DeleteCert(ctx context.Context, name string) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM certs WHERE check_name = ?", name); err != nil {
		return fmt.Errorf("store: delete cert: %w", err)
	}
	return nil
}
