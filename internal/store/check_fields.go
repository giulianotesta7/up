package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/secret"
)

// checkColumns lists the checks columns in the order checkFields.values and
// scanCheck use. checkPlaceholders has one "?" per column.
const (
	checkColumns      = "name, kind, url, interval_ns, timeout_ns, expected_status, channel, alert_after, recover_after, tls_off, method, headers, body, body_key, expect_text, host, port"
	checkPlaceholders = "?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?"
)

// headerRow is the stored form of one header inside the headers column. The
// encoding is deterministic, so the stale-result guard compares it as text.
type headerRow struct {
	Name  string `json:"n"`
	Value []byte `json:"v,omitempty"`
	Key   []byte `json:"k,omitempty"`
}

func encodeHeaders(headers []config.Header) (string, error) {
	if len(headers) == 0 {
		return "", nil
	}
	rows := make([]headerRow, len(headers))
	for i, h := range headers {
		rows[i] = headerRow{Name: h.Name, Value: h.Value.Data, Key: h.Value.Key}
	}
	b, err := json.Marshal(rows)
	return string(b), err
}

func decodeHeaders(text string) ([]config.Header, error) {
	if text == "" {
		return nil, nil
	}
	var rows []headerRow
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		return nil, fmt.Errorf("stored headers are unreadable: %w", err)
	}
	headers := make([]config.Header, len(rows))
	for i, r := range rows {
		headers[i] = config.Header{Name: r.Name, Value: secret.Sealed{Key: r.Key, Data: r.Value}}
	}
	return headers, nil
}

// protectedValues are the stored values an update may keep.
type protectedValues struct {
	headers []config.Header
	body    secret.Sealed
}

// fields are a check's resolved columns beyond the plain ones.
type fields struct {
	headers string
	body    secret.Sealed
	kind    string
}

// checkFields resolves c's protected values into stored form. stored is nil
// when creating: keep and replace store the given value and clear stores
// none. On update, keep retains the stored value instead (a header is matched
// by name, case-insensitively, each stored header at most once, and a header
// with nothing stored to keep is empty).
func checkFields(c config.Check, stored *protectedValues) (fields, error) {
	headers := make([]config.Header, len(c.Headers))
	var used []bool
	if stored != nil {
		used = make([]bool, len(stored.headers))
	}
	for i, h := range c.Headers {
		out := config.Header{Name: h.Name}
		switch {
		case h.Edit == config.EditClear:
		case h.Edit == config.EditKeep && stored != nil:
			for j, old := range stored.headers {
				if !used[j] && strings.EqualFold(old.Name, h.Name) {
					used[j], out.Value = true, old.Value
					break
				}
			}
		default:
			out.Value = h.Value
		}
		headers[i] = out
	}
	body := c.Body
	switch {
	case c.BodyEdit == config.EditClear:
		body = secret.Sealed{}
	case c.BodyEdit == config.EditKeep && stored != nil:
		body = stored.body
	}
	text, err := encodeHeaders(headers)
	if err != nil {
		return fields{}, err
	}
	kind := c.Kind
	if kind == "" {
		kind = config.CheckHTTP
	}
	return fields{headers: text, body: body, kind: kind}, nil
}

// values lists the column values in checkColumns order.
func (f fields) values(c config.Check) []any {
	return []any{c.Name, f.kind, c.URL, int64(c.Interval), int64(c.Timeout), c.ExpectedStatus, c.Channel, c.AlertAfter, c.RecoverAfter, c.TLSOff,
		c.Method, f.headers, blob(f.body.Data), blob(f.body.Key), c.ExpectText, c.Host, c.Port}
}

func scanCheck(row scanner) (config.Check, error) {
	var c config.Check
	var kind, headers string
	var interval, timeout int64
	var body, bodyKey []byte
	err := row.Scan(&c.Name, &kind, &c.URL, &interval, &timeout, &c.ExpectedStatus, &c.Channel, &c.AlertAfter, &c.RecoverAfter, &c.TLSOff,
		&c.Method, &headers, &body, &bodyKey, &c.ExpectText, &c.Host, &c.Port)
	if err != nil {
		return config.Check{}, err
	}
	if kind != config.CheckHTTP {
		c.Kind = kind
	}
	c.Interval, c.Timeout = time.Duration(interval), time.Duration(timeout)
	if c.Headers, err = decodeHeaders(headers); err != nil {
		return config.Check{}, err
	}
	if len(body) > 0 {
		c.Body = secret.Sealed{Key: bodyKey, Data: body}
	}
	return c, nil
}
