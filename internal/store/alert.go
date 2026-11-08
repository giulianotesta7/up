package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/secret"
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
		return classify(ErrValidation, fmt.Errorf("channel %q does not exist", name))
	}
	return nil
}
func requireSMTP(ctx context.Context, tx *sql.Tx, name string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM smtp_servers WHERE name = ?)", name).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return classify(ErrValidation, fmt.Errorf("SMTP server %q does not exist", name))
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
		return classify(ErrValidation, err)
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
		return classify(ErrNotFound, fmt.Errorf("name %q not found", v.Name))
	}
	if !update && exists {
		return classify(ErrExists, fmt.Errorf("name %q already exists", v.Name))
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

const channelColumns = "name, kind, smtp_server, recipients, webhook_origin, webhook_url, webhook_url_key, webhook_auth_name, webhook_auth, webhook_auth_key, server_url, topic, chat_id, token, token_key, priority"

func scanChannel(row scanner) (config.Channel, error) {
	var c config.Channel
	var recipients string
	var url, urlKey, auth, authKey, token, tokenKey []byte
	err := row.Scan(&c.Name, &c.Kind, &c.SMTPServer, &recipients, &c.WebhookOrigin, &url, &urlKey, &c.WebhookAuthName, &auth, &authKey,
		&c.ServerURL, &c.Topic, &c.ChatID, &token, &tokenKey, &c.Priority)
	if recipients != "" {
		c.Recipients = strings.Split(recipients, "\n")
	}
	if len(url) > 0 {
		c.WebhookURL = secret.Sealed{Key: urlKey, Data: url}
	}
	if len(auth) > 0 {
		c.WebhookAuth = secret.Sealed{Key: authKey, Data: auth}
	}
	if len(token) > 0 {
		c.Token = secret.Sealed{Key: tokenKey, Data: token}
	}
	return c, err
}

// Channels returns channels ordered by name. Webhook URLs, auth values and
// tokens come back sealed; callers that expose channels must strip them.
func (s *Store) Channels(ctx context.Context) ([]config.Channel, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+channelColumns+" FROM channels ORDER BY name")
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
	c, err := scanChannel(s.db.QueryRowContext(ctx, "SELECT "+channelColumns+" FROM channels WHERE name=?", name))
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
		return classify(ErrValidation, err)
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
		return classify(ErrNotFound, fmt.Errorf("name %q not found", c.Name))
	}
	if !update && exists {
		return classify(ErrExists, fmt.Errorf("name %q already exists", c.Name))
	}
	// Protected values: resolve keep/replace/clear against the stored ones
	// (config.Edit). Keep only finds a value stored for the same kind, so a
	// kind switch needs new secrets. Email channels store none.
	var old config.Channel
	if c.Kind == config.KindEmail {
		if err = requireSMTP(ctx, tx, c.SMTPServer); err != nil {
			return
		}
	} else if update {
		if old, err = scanChannel(tx.QueryRowContext(ctx, "SELECT "+channelColumns+" FROM channels WHERE name=?", c.Name)); err != nil {
			return
		}
		if old.Kind != c.Kind {
			old = config.Channel{}
		}
	}
	var url, auth, token secret.Sealed
	origin, authName := "", ""
	switch c.Kind {
	case config.KindWebhook, config.KindDiscord, config.KindSlack:
		url = resolveProtected(c.WebhookURLEdit, c.WebhookURL, old.WebhookURL, update)
		origin = c.WebhookOrigin
		what := "webhook URL"
		if c.Kind != config.KindWebhook {
			what = c.Kind + " webhook URL"
		}
		if url.Empty() {
			return classify(ErrValidation, fmt.Errorf("%s is required", what))
		}
	case config.KindNtfy, config.KindTelegram, config.KindGotify:
		token = resolveProtected(c.TokenEdit, c.Token, old.Token, update)
		switch {
		case c.Kind == config.KindTelegram && token.Empty():
			return classify(ErrValidation, errors.New("telegram bot token is required"))
		case c.Kind == config.KindGotify && token.Empty():
			return classify(ErrValidation, errors.New("gotify app token is required"))
		}
	}
	if c.Kind == config.KindWebhook {
		auth = resolveProtected(c.WebhookAuthEdit, c.WebhookAuth, old.WebhookAuth, update)
		authName = c.WebhookAuthName
		if authName == "" && !auth.Empty() {
			return classify(ErrValidation, errors.New("webhook auth value requires a header name"))
		}
	}
	values := []any{c.Kind, c.SMTPServer, strings.Join(c.Recipients, "\n"), origin, blob(url.Data), blob(url.Key), authName, blob(auth.Data), blob(auth.Key),
		c.ServerURL, c.Topic, c.ChatID, blob(token.Data), blob(token.Key), c.Priority, c.Name}
	const columns = "kind, smtp_server, recipients, webhook_origin, webhook_url, webhook_url_key, webhook_auth_name, webhook_auth, webhook_auth_key, server_url, topic, chat_id, token, token_key, priority"
	if update {
		_, err = tx.ExecContext(ctx, "UPDATE channels SET "+strings.ReplaceAll(columns, ",", "=?,")+"=? WHERE name=?", values...)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO channels ("+columns+", name) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", values...)
	}
	if err != nil {
		return
	}
	return tx.Commit()
}

// resolveProtected applies an edit to one protected value (see config.Edit).
// Without a stored value (creating), keep stores the given one.
func resolveProtected(edit config.Edit, given, stored secret.Sealed, update bool) secret.Sealed {
	switch {
	case edit == config.EditClear:
		return secret.Sealed{}
	case edit == config.EditKeep && update:
		return stored
	}
	return given
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
		return classify(ErrNotFound, fmt.Errorf("name %q not found", name))
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
	if table == "channels" {
		if _, err = tx.ExecContext(ctx, "DELETE FROM channel_tests WHERE channel=?", name); err != nil {
			return
		}
		// A deleted default channel makes the default "none".
		if _, err = tx.ExecContext(ctx, "UPDATE defaults SET channel='' WHERE channel=?", name); err != nil {
			return
		}
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
