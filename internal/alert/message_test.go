package alert

import (
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestMessageBytes(t *testing.T) {
	m := Message{From: "Up <alerts@example.com>", To: []string{"a@example.com", "b@example.com"}, Subject: "café\r\nBcc: x@y", Body: "hello\nworld = café\n", Date: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	b, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(strings.ReplaceAll(s, "\r\n", ""), "\n") {
		t.Fatal("bare newline")
	}
	lines := strings.Split(s, "\r\n")
	for i, h := range []string{"From:", "To:", "Subject:", "Date:", "Message-ID:", "MIME-Version:", "Content-Type:", "Content-Transfer-Encoding:"} {
		if !strings.HasPrefix(lines[i], h) {
			t.Fatalf("header %d: %q", i, lines[i])
		}
	}
	if strings.Contains(s, "\r\nBcc:") || !strings.Contains(s, "=?utf-8?q?") {
		t.Fatalf("unsafe or unencoded subject: %s", s)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(s))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || decoded != "café  Bcc: x@y" {
		t.Fatalf("subject %q: %v", decoded, err)
	}
	body, err := io.ReadAll(quotedprintable.NewReader(parsed.Body))
	if err != nil || strings.ReplaceAll(string(body), "\r\n", "\n") != m.Body {
		t.Fatalf("body %q: %v", body, err)
	}
}

func TestMessageInvalidAddresses(t *testing.T) {
	for _, m := range []Message{{From: "bad", To: []string{"a@example.com"}}, {From: "a@example.com\r\nX: y", To: []string{"a@example.com"}}, {From: "a@example.com", To: []string{"bad"}}, {From: "a@example.com", To: []string{"a@example.com\nX: y"}}, {From: "a@example.com"}} {
		if _, err := m.Bytes(); err == nil {
			t.Fatalf("accepted %#v", m)
		}
	}
}
