package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/giulianotesta7/up/internal/config"
)

// InUseError reports a deletion refused because other objects use the target.
type InUseError struct {
	Kind      string
	Name      string
	Users     []string
	UsersKind string
}

// Reason describes the objects using the target.
func (e *InUseError) Reason() string {
	kind := e.UsersKind
	if len(e.Users) > 1 {
		kind += "s"
	}
	return "used by " + kind + " " + strings.Join(e.Users, ", ")
}
func (e *InUseError) Error() string {
	return fmt.Sprintf("store: delete %s: %q is %s", e.Kind, e.Name, e.Reason())
}

func requireChannel(ctx context.Context, tx *sql.Tx, name string) error {
	if name == "" {
		return nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM channels WHERE name = ?)", name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("channel %q does not exist", name)
	}
	return nil
}
func requireSMTP(ctx context.Context, tx *sql.Tx, name string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM smtp_servers WHERE name = ?)", name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("SMTP server %q does not exist", name)
	}
	return nil
}
func blob(b []byte) []byte {
	if len(b) == 0 {
		return []byte{}
	}
	return b
}

type scanner interface{ Scan(...any) error }

func scanSMTP(row scanner) (config.SMTPServer, error) {
	var s config.SMTPServer
	err := row.Scan(&s.Name, &s.Host, &s.Port, &s.TLS, &s.Username, &s.Password.Data, &s.Password.Key, &s.Sender)
	return s, err
}

const smtpColumns = "name, host, port, tls, username, password, password_key, sender"

// SMTPServers returns servers ordered by name.
func (s *Store) SMTPServers(ctx context.Context) ([]config.SMTPServer, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+smtpColumns+" FROM smtp_servers ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("store: SMTP servers: %w", err)
	}
	defer rows.Close()
	var out []config.SMTPServer
	for rows.Next() {
		v, err := scanSMTP(rows)
		if err != nil {
			return nil, fmt.Errorf("store: SMTP servers: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: SMTP servers: %w", err)
	}
	return out, nil
}

// SMTPServer finds a server by name.
func (s *Store) SMTPServer(ctx context.Context, name string) (config.SMTPServer, bool, error) {
	v, err := scanSMTP(s.db.QueryRowContext(ctx, "SELECT "+smtpColumns+" FROM smtp_servers WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return config.SMTPServer{}, false, nil
	}
	if err != nil {
		return config.SMTPServer{}, false, fmt.Errorf("store: SMTP server: %w", err)
	}
	return v, true, nil
}

// AddSMTPServer inserts a validated server.
func (s *Store) AddSMTPServer(ctx context.Context, v config.SMTPServer) error {
	return s.writeSMTP(ctx, v, false)
}

// UpdateSMTPServer updates a server, preserving an omitted password for authenticated servers.
func (s *Store) UpdateSMTPServer(ctx context.Context, v config.SMTPServer) error {
	return s.writeSMTP(ctx, v, true)
}
func (s *Store) writeSMTP(ctx context.Context, v config.SMTPServer, update bool) (err error) {
	op := "add SMTP server"
	if update {
		op = "update SMTP server"
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("store: %s: %w", op, err)
		}
	}()
	if err = config.ValidateSMTPServer(v); err != nil {
		return
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM smtp_servers WHERE name = ?)", v.Name).Scan(&exists); err != nil {
		return
	}
	if update && !exists {
		return fmt.Errorf("name %q not found", v.Name)
	}
	if !update && exists {
		return fmt.Errorf("name %q already exists", v.Name)
	}
	data, key := blob(v.Password.Data), blob(v.Password.Key)
	if update && v.Username == "" {
		data, key = []byte{}, []byte{}
	} else if update && v.Password.Empty() {
		if err = tx.QueryRowContext(ctx, "SELECT password, password_key FROM smtp_servers WHERE name = ?", v.Name).Scan(&data, &key); err != nil {
			return
		}
		data, key = blob(data), blob(key)
	}
	if update {
		_, err = tx.ExecContext(ctx, "UPDATE smtp_servers SET host=?, port=?, tls=?, username=?, password=?, password_key=?, sender=? WHERE name=?", v.Host, v.Port, v.TLS, v.Username, data, key, v.Sender, v.Name)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO smtp_servers (name, host, port, tls, username, password, password_key, sender) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", v.Name, v.Host, v.Port, v.TLS, v.Username, data, key, v.Sender)
	}
	if err != nil {
		return
	}
	return tx.Commit()
}

func scanChannel(row scanner) (config.Channel, error) {
	var c config.Channel
	var recipients string
	err := row.Scan(&c.Name, &c.Kind, &c.SMTPServer, &recipients)
	if recipients != "" {
		c.Recipients = strings.Split(recipients, "\n")
	}
	return c, err
}

// Channels returns channels ordered by name.
func (s *Store) Channels(ctx context.Context) ([]config.Channel, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name, kind, smtp_server, recipients FROM channels ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("store: channels: %w", err)
	}
	defer rows.Close()
	var out []config.Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, fmt.Errorf("store: channels: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: channels: %w", err)
	}
	return out, nil
}

// Channel finds a channel by name.
func (s *Store) Channel(ctx context.Context, name string) (config.Channel, bool, error) {
	c, err := scanChannel(s.db.QueryRowContext(ctx, "SELECT name, kind, smtp_server, recipients FROM channels WHERE name=?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return config.Channel{}, false, nil
	}
	if err != nil {
		return config.Channel{}, false, fmt.Errorf("store: channel: %w", err)
	}
	return c, true, nil
}

// AddChannel inserts a validated channel.
func (s *Store) AddChannel(ctx context.Context, c config.Channel) error {
	return s.writeChannel(ctx, c, false)
}

// UpdateChannel updates a validated channel.
func (s *Store) UpdateChannel(ctx context.Context, c config.Channel) error {
	return s.writeChannel(ctx, c, true)
}
func (s *Store) writeChannel(ctx context.Context, c config.Channel, update bool) (err error) {
	op := "add channel"
	if update {
		op = "update channel"
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("store: %s: %w", op, err)
		}
	}()
	if err = config.ValidateChannel(c); err != nil {
		return
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM channels WHERE name=?)", c.Name).Scan(&exists); err != nil {
		return
	}
	if update && !exists {
		return fmt.Errorf("name %q not found", c.Name)
	}
	if !update && exists {
		return fmt.Errorf("name %q already exists", c.Name)
	}
	if err = requireSMTP(ctx, tx, c.SMTPServer); err != nil {
		return
	}
	if update {
		_, err = tx.ExecContext(ctx, "UPDATE channels SET kind=?, smtp_server=?, recipients=? WHERE name=?", c.Kind, c.SMTPServer, strings.Join(c.Recipients, "\n"), c.Name)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO channels (name, kind, smtp_server, recipients) VALUES (?, ?, ?, ?)", c.Name, c.Kind, c.SMTPServer, strings.Join(c.Recipients, "\n"))
	}
	if err != nil {
		return
	}
	return tx.Commit()
}

// DeleteSMTPServer removes a server unless channels use it.
func (s *Store) DeleteSMTPServer(ctx context.Context, name string) error {
	return s.deleteAlert(ctx, name, "SMTP server")
}

// DeleteChannel removes a channel unless monitors use it.
func (s *Store) DeleteChannel(ctx context.Context, name string) error {
	return s.deleteAlert(ctx, name, "channel")
}
func (s *Store) deleteAlert(ctx context.Context, name, kind string) (err error) {
	op := "delete " + kind
	defer func() {
		if err != nil {
			var in *InUseError
			if !errors.As(err, &in) {
				err = fmt.Errorf("store: %s: %w", op, err)
			}
		}
	}()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	table, query, userKind := "channels", "SELECT name FROM checks WHERE channel=? ORDER BY name", "monitor"
	if kind == "SMTP server" {
		table, query, userKind = "smtp_servers", "SELECT name FROM channels WHERE smtp_server=? ORDER BY name", "channel"
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE name=?)", name).Scan(&exists); err != nil {
		return
	}
	if !exists {
		return fmt.Errorf("name %q not found", name)
	}
	rows, e := tx.QueryContext(ctx, query, name)
	if e != nil {
		return e
	}
	var users []string
	for rows.Next() {
		var n string
		if e = rows.Scan(&n); e != nil {
			rows.Close()
			return e
		}
		users = append(users, n)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(users) > 0 {
		return &InUseError{Kind: kind, Name: name, Users: users, UsersKind: userKind}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE name=?", name); err != nil {
		return
	}
	return tx.Commit()
}

// PublicKey returns the daemon's public key, if present.
func (s *Store) PublicKey(ctx context.Context) ([]byte, bool, error) {
	var key []byte
	err := s.db.QueryRowContext(ctx, "SELECT public_key FROM secret_key WHERE id=1").Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: public key: %w", err)
	}
	return key, true, nil
}

// SetPublicKey replaces the daemon's public key.
func (s *Store) SetPublicKey(ctx context.Context, key []byte) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO secret_key (id, public_key) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET public_key=excluded.public_key", blob(key))
	if err != nil {
		return fmt.Errorf("store: set public key: %w", err)
	}
	return nil
}
