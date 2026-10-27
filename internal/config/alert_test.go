package config

import (
	"github.com/giulianotesta7/up/internal/secret"

	"reflect"
	"testing"
)

func TestAlertValidation(t *testing.T) {
	base := SMTPServer{Name: "s", Host: "smtp.example.com", Port: 25, TLS: TLSStartTLS, Sender: "a@example.com"}
	tests := []struct {
		name   string
		mutate func(*SMTPServer)
		want   string
	}{
		{"name", func(s *SMTPServer) { s.Name = "" }, "name is required"}, {"host", func(s *SMTPServer) { s.Host = "" }, "host is required"},
		{"scheme", func(s *SMTPServer) { s.Host = "http://host" }, `host "http://host" must be a host name or IP address, without scheme or port`},
		{"host port", func(s *SMTPServer) { s.Host = "host:25" }, `host "host:25" must be a host name or IP address, without scheme or port`},
		{"port 0", func(s *SMTPServer) { s.Port = 0 }, "port 0 must be between 1 and 65535"}, {"port 1", func(s *SMTPServer) { s.Port = 1 }, ""}, {"port 65535", func(s *SMTPServer) { s.Port = 65535 }, ""}, {"port 65536", func(s *SMTPServer) { s.Port = 65536 }, "port 65536 must be between 1 and 65535"},
		{"starttls", func(s *SMTPServer) { s.TLS = TLSStartTLS }, ""}, {"tls", func(s *SMTPServer) { s.TLS = TLSImplicit }, ""}, {"none", func(s *SMTPServer) { s.TLS = TLSNone }, ""}, {"none auth", func(s *SMTPServer) { s.TLS = TLSNone; s.Username = "u" }, `TLS "none" cannot be used with a username (the password would travel in plain text)`}, {"bad tls", func(s *SMTPServer) { s.TLS = "bad" }, `TLS "bad" must be starttls, tls or none`},
		{"sender missing", func(s *SMTPServer) { s.Sender = "" }, "sender is required"}, {"sender bad", func(s *SMTPServer) { s.Sender = "bad" }, `sender "bad" is not a valid email address`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := base
			tt.mutate(&s)
			err := ValidateSMTPServer(s)
			if (err == nil) != (tt.want == "") || err != nil && err.Error() != tt.want {
				t.Fatalf("got %v want %q", err, tt.want)
			}
		})
	}
	c := Channel{Name: "c", Kind: KindEmail, SMTPServer: "s", Recipients: []string{"a@example.com"}}
	cases := []struct {
		name   string
		mutate func(*Channel)
		want   string
	}{
		{"valid", func(*Channel) {}, ""}, {"name", func(c *Channel) { c.Name = "" }, "name is required"}, {"kind", func(c *Channel) { c.Kind = "sms" }, `kind "sms" is not supported (use email)`}, {"server", func(c *Channel) { c.SMTPServer = "" }, "SMTP server is required"}, {"recipients", func(c *Channel) { c.Recipients = nil }, "at least one recipient is required"}, {"display", func(c *Channel) { c.Recipients = []string{"Name <a@example.com>"} }, `recipient "Name <a@example.com>" is not a valid email address`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			v := c
			tt.mutate(&v)
			err := ValidateChannel(v)
			if (err == nil) != (tt.want == "") || err != nil && err.Error() != tt.want {
				t.Fatalf("got %v want %q", err, tt.want)
			}
		})
	}
	if got := ParseRecipients(" , a@example.com, , b@example.com ,, "); !reflect.DeepEqual(got, []string{"a@example.com", "b@example.com"}) {
		t.Fatal(got)
	}
	if got := ParseRecipients(" , "); got != nil {
		t.Fatal(got)
	}
}

func TestValidateSMTPServerRejectsPasswordWithoutUsername(t *testing.T) {
	s := SMTPServer{Name: "relay", Host: "localhost", Port: 25, TLS: TLSNone, Sender: "up@example.com", Password: secret.Sealed{Key: []byte{1}, Data: []byte{2}}}
	if err := ValidateSMTPServer(s); err == nil || err.Error() != "password requires a username" {
		t.Fatalf("ValidateSMTPServer() error = %v, want password requires a username", err)
	}
	s.Password = secret.Sealed{}
	if err := ValidateSMTPServer(s); err != nil {
		t.Fatalf("ValidateSMTPServer() without username or password error = %v", err)
	}
}

func TestValidateChannelReservesNone(t *testing.T) {
	c := Channel{Name: "none", Kind: KindEmail, SMTPServer: "gmail", Recipients: []string{"a@example.com"}}
	if err := ValidateChannel(c); err == nil || err.Error() != `name "none" is reserved (it means no channel)` {
		t.Fatalf("ValidateChannel() error = %v, want reserved-name error", err)
	}
}
