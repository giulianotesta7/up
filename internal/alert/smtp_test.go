package alert

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/giulianotesta7/up/internal/config"
)

type transcript struct {
	commands   []string
	auth, data string
	authTLS    bool
}

func cert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "smtp.test"}, DNSNames: []string{"smtp.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	parsed, e := x509.ParseCertificate(der)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}
func fake(t *testing.T, start, auth, implicit bool) (func(context.Context, string, string) (net.Conn, error), <-chan transcript, *x509.CertPool) {
	t.Helper()
	certificate, roots := cert(t)
	done := make(chan transcript, 1)
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		a, b := net.Pipe()
		deadline := time.Now().Add(5 * time.Second)
		a.SetDeadline(deadline)
		b.SetDeadline(deadline)
		go func() {
			defer b.Close()
			var c net.Conn = b
			secure := false
			result := transcript{}
			defer func() { done <- result }()
			if implicit {
				c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{certificate}})
				secure = true
			}
			r := bufio.NewReader(c)
			fmt.Fprint(c, "220 smtp.test ESMTP\r\n")
			for {
				line, e := r.ReadString('\n')
				if e != nil {
					return
				}
				line = strings.TrimSpace(line)
				result.commands = append(result.commands, line)
				switch {
				case strings.HasPrefix(line, "EHLO"):
					fmt.Fprint(c, "250-smtp.test\r\n")
					if start && !secure {
						fmt.Fprint(c, "250-STARTTLS\r\n")
					}
					if auth {
						fmt.Fprint(c, "250-AUTH PLAIN\r\n")
					}
					fmt.Fprint(c, "250 OK\r\n")
				case line == "STARTTLS":
					fmt.Fprint(c, "220 ready\r\n")
					c = tls.Server(c, &tls.Config{Certificates: []tls.Certificate{certificate}})
					r = bufio.NewReader(c)
					secure = true
				case strings.HasPrefix(line, "AUTH PLAIN "):
					result.authTLS = secure
					v, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
					result.auth = string(v)
					fmt.Fprint(c, "235 ok\r\n")
				case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
					fmt.Fprint(c, "250 ok\r\n")
				case line == "DATA":
					fmt.Fprint(c, "354 go\r\n")
					var data strings.Builder
					for {
						l, e := r.ReadString('\n')
						if e != nil {
							return
						}
						if l == ".\r\n" {
							break
						}
						data.WriteString(l)
					}
					result.data = data.String()
					fmt.Fprint(c, "250 ok\r\n")
				case line == "QUIT":
					fmt.Fprint(c, "221 bye\r\n")
					if secure {
						var buf [1]byte
						c.Read(buf[:])
						c.Close()
					}
					return
				default:
					fmt.Fprint(c, "500 unexpected\r\n")
				}
			}
		}()
		return a, nil
	}
	return dial, done, roots
}
func TestSMTPModes(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		mode                  config.TLSMode
		start, auth, implicit bool
		user, want            string
	}{{"starttls", config.TLSStartTLS, true, true, false, "user", ""}, {"missing starttls", config.TLSStartTLS, false, true, false, "user", "does not offer STARTTLS"}, {"implicit", config.TLSImplicit, false, true, true, "user", ""}, {"plain", config.TLSNone, false, false, false, "", ""}, {"missing auth", config.TLSStartTLS, true, false, false, "user", "does not offer AUTH"}} {
		t.Run(tc.name, func(t *testing.T) {
			dial, done, roots := fake(t, tc.start, tc.auth, tc.implicit)
			sender := SMTPSender{Dial: dial, TLSConfig: &tls.Config{RootCAs: roots}}
			msg := Message{From: "Up <from@example.com>", To: []string{"a@example.com", "b@example.com"}, Subject: "test", Body: "hello\n", Date: time.Now()}
			err := sender.Send(context.Background(), config.SMTPServer{Host: "smtp.test", Port: 25, TLS: tc.mode, Username: tc.user}, "secret", msg)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error: %v", err)
			}
			var tr transcript
			select {
			case tr = <-done:
			case <-time.After(6 * time.Second):
				t.Fatal("server stuck")
			}
			commands := strings.Join(tr.commands, "|")
			if tc.want != "" {
				if strings.Contains(commands, "MAIL") || strings.Contains(commands, "AUTH ") {
					t.Fatal(commands)
				}
				return
			}
			if !strings.Contains(commands, "MAIL FROM:<from@example.com>") || !strings.Contains(commands, "RCPT TO:<a@example.com>") || !strings.Contains(commands, "RCPT TO:<b@example.com>") || !strings.Contains(tr.data, "hello") {
				t.Fatalf("%s %s", commands, tr.data)
			}
			if tc.user != "" && (tr.auth != "\x00user\x00secret" || !tr.authTLS) {
				t.Fatalf("auth %#v", tr)
			}
			if tc.user == "" && strings.Contains(commands, "AUTH ") {
				t.Fatal(commands)
			}
			if tc.mode == config.TLSStartTLS && strings.Index(commands, "STARTTLS") > strings.Index(commands, "AUTH PLAIN") {
				t.Fatal(commands)
			}
		})
	}
}
func TestRefusePlainAuth(t *testing.T) {
	called := false
	s := SMTPSender{Dial: func(context.Context, string, string) (net.Conn, error) { called = true; return nil, io.EOF }}
	err := s.Send(context.Background(), config.SMTPServer{Host: "smtp.test", Port: 25, TLS: config.TLSNone, Username: "u"}, "p", Message{})
	if err == nil || !strings.Contains(err.Error(), "without TLS") || called {
		t.Fatalf("%v called=%v", err, called)
	}
}
func TestCancel(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	s := SMTPSender{Dial: func(context.Context, string, string) (net.Conn, error) { return a, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- s.Send(ctx, config.SMTPServer{Host: "smtp.test", Port: 25, TLS: config.TLSNone}, "", Message{})
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock")
	}
}
