package alert

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// Message is one alert email.
type Message struct {
	From    string
	To      []string
	Subject string
	Body    string
	Date    time.Time
}

// Bytes renders the message as RFC 5322 text with CRLF line endings.
func (m Message) Bytes() ([]byte, error) {
	parse := func(s string) (*mail.Address, error) {
		if strings.ContainsAny(s, "\r\n") {
			return nil, errors.New("address contains newline")
		}
		return mail.ParseAddress(s)
	}
	from, err := parse(m.From)
	if err != nil {
		return nil, fmt.Errorf("invalid from: %w", err)
	}
	if len(m.To) == 0 {
		return nil, errors.New("at least one recipient is required")
	}
	recipients := make([]string, 0, len(m.To))
	for _, s := range m.To {
		a, e := parse(s)
		if e != nil {
			return nil, fmt.Errorf("invalid recipient: %w", e)
		}
		if a.Name != "" || a.Address != s {
			return nil, errors.New("recipient must be a bare address")
		}
		recipients = append(recipients, a.Address)
	}
	id := make([]byte, 16)
	if _, err = rand.Read(id); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	subject := strings.NewReplacer("\r", " ", "\n", " ").Replace(m.Subject)
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@up>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", from.String(), strings.Join(recipients, ", "), mime.QEncoding.Encode("utf-8", subject), m.Date.Format(time.RFC1123Z), hex.EncodeToString(id))
	w := quotedprintable.NewWriter(&b)
	body := strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n")
	if _, err = w.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
