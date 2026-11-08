package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heohlee/up/internal/config"
	"github.com/heohlee/up/internal/remote"
	"github.com/heohlee/up/internal/secret"
	"github.com/heohlee/up/internal/store"
)

func TestAPIRejectsUnsafeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"--api-listen", "127.0.0.1:0", "--tls-cert", "cert"}, "--tls-cert and --tls-key must be provided together"},
		{[]string{"--api-listen", "127.0.0.1:0", "--tls-key", "key"}, "--tls-cert and --tls-key must be provided together"},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "up.db")
		args := append([]string{"serve", "--db", path}, tc.args...)
		code, out, errout := runCmd(context.Background(), args...)
		if code != 1 || out != "" || errout != "up: "+tc.message+"\n" {
			t.Errorf("got %d %q %q", code, out, errout)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Errorf("changed files: %v %v", entries, err)
		}
	}
}

func TestTokenResetCLI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db")
	code, out, errout := runCmd(context.Background(), "token", "reset", "--db", path)
	if code != 0 || !strings.HasPrefix(out, "up_") || !strings.HasSuffix(out, "\n") || errout != "up: access token shown once; store it securely\n" {
		t.Fatalf("got %d %q %q", code, out, errout)
	}
	old := strings.TrimSuffix(out, "\n")
	code, out, errout = runCmd(context.Background(), "token", "reset", "--db", path)
	if code != 0 || errout != "up: access token shown once; store it securely\n" {
		t.Fatalf("got %d %q %q", code, out, errout)
	}
	v, err := remote.LoadVerifier(path + ".token")
	if err != nil || v.Verify(old) || !v.Verify(strings.TrimSuffix(out, "\n")) {
		t.Fatalf("reset verification: %v", err)
	}
	info, err := os.Stat(path + ".token")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v %v", info, err)
	}
}

func TestAPIServeAndReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db")
	var token string
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithCancel(context.Background())
		reader, writer := io.Pipe()
		lines := make(chan string, 32)
		go func() {
			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				lines <- scanner.Text()
			}
			close(lines)
		}()
		done := make(chan int, 1)
		go func() {
			code := run(ctx, []string{"serve", "--db", path, "--api-listen", "0.0.0.0:0"}, io.Discard, writer)
			writer.Close()
			done <- code
		}()
		address := ""
		foundToken := ""
		timeout := time.After(5 * time.Second)
		for address == "" {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatal("serve stopped before listening")
				}
				if strings.HasPrefix(line, "up: access token (shown once): ") {
					foundToken = strings.TrimPrefix(line, "up: access token (shown once): ")
				}
				if strings.HasPrefix(line, "up: API listening on ") {
					address = strings.TrimPrefix(line, "up: API listening on ")
				}
			case <-timeout:
				t.Fatal("serve did not listen")
			}
		}
		if attempt == 0 {
			token = foundToken
			if !strings.HasPrefix(token, "up_") {
				t.Fatalf("token %q", token)
			}
		} else if foundToken != "" {
			t.Fatalf("token printed again: %q", foundToken)
		}
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			t.Fatal(err)
		}
		address = net.JoinHostPort("127.0.0.1", port)
		ping := func(tok string, want int) {
			req, _ := http.NewRequest("GET", "http://"+address+"/api/v1/ping", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != want {
				t.Fatalf("status %d want %d", res.StatusCode, want)
			}
		}
		ping(token, 200)
		ping("wrong", 401)
		if attempt == 1 {
			code, out, stderr := runCmd(context.Background(), "token", "reset", "--db", path)
			if code != 0 || stderr != "up: access token shown once; store it securely\n" {
				t.Fatalf("reset: %d %q", code, stderr)
			}
			ping(token, 401)
			ping(strings.TrimSpace(out), 200)
		}
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Fatalf("serve exit %d", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("shutdown timed out")
		}
		reader.Close()
	}
}

func TestAPISnapshotServe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db")
	k, _, err := secret.LoadOrCreate(path + ".key")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := secret.Seal(k.PublicKey(), "password")
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = s.AddCheck(ctx, config.Check{Name: "snapshot-site", URL: "https://example.com", Interval: time.Hour, Timeout: time.Second, ExpectedStatus: 200}); err != nil {
		t.Fatal(err)
	}
	if err = s.AddSMTPServer(ctx, config.SMTPServer{Name: "snapshot-smtp", Host: "smtp.example.com", Port: 465, TLS: config.TLSImplicit, Username: "user", Sender: "a@example.com", Password: sealed}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	serveCtx, cancel := context.WithCancel(ctx)
	reader, writer := io.Pipe()
	lines := make(chan string, 64)
	done := make(chan int, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	go func() {
		done <- run(serveCtx, []string{"serve", "--db", path, "--api-listen", "127.0.0.1:0"}, io.Discard, writer)
		writer.Close()
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("shutdown timed out")
		}
		reader.Close()
	}()
	var token, address string
	timeout := time.After(5 * time.Second)
	for address == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("serve stopped")
			}
			if strings.HasPrefix(line, "up: access token (shown once): ") {
				token = strings.TrimPrefix(line, "up: access token (shown once): ")
			}
			if strings.HasPrefix(line, "up: API listening on ") {
				address = strings.TrimPrefix(line, "up: API listening on ")
			}
		case <-timeout:
			t.Fatal("serve timeout")
		}
	}
	req, _ := http.NewRequest("GET", "http://"+address+"/api/v1/snapshot", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Services []struct {
			Check struct {
				Name string `json:"name"`
			} `json:"check"`
		} `json:"services"`
		SMTP []struct {
			Name          string `json:"name"`
			PasswordState string `json:"password_state"`
		} `json:"smtp_servers"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || len(doc.Services) != 1 || doc.Services[0].Check.Name != "snapshot-site" || len(doc.SMTP) != 1 || doc.SMTP[0].Name != "snapshot-smtp" || doc.SMTP[0].PasswordState != "set" {
		t.Fatalf("snapshot: %d %s", res.StatusCode, raw)
	}
	if bytes.Contains(raw, sealed.Data) || bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(sealed.Data))) {
		t.Fatal("ciphertext leaked")
	}
}

func TestAPIHelp(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		contains []string
	}{{[]string{"-h"}, []string{"token"}}, {[]string{"serve", "-h"}, []string{"api-listen", "tls-cert", "tls-key", "<db>.token"}}, {[]string{"token", "-h"}, []string{"reset", "shown once", "<db>.token"}}} {
		code, out, errout := runCmd(context.Background(), tc.args...)
		if code != 0 || errout != "" {
			t.Fatalf("%v: %d %q", tc.args, code, errout)
		}
		for _, s := range tc.contains {
			if !strings.Contains(out, s) {
				t.Errorf("%v missing %s", tc.args, s)
			}
		}
	}
	code, out, errout := runCmd(context.Background(), "token", "reset", "--nope")
	if code != 2 || out != "" || !strings.Contains(errout, "flag provided but not defined: -nope") {
		t.Fatal(fmt.Sprint(code, out, errout))
	}
}

func TestServeWithoutAPIFlagCreatesNoTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	code, _, errout := runCmd(ctx, "serve", "--db", path)
	if code != 0 || strings.Contains(errout, "API listening") || strings.Contains(errout, "access token") {
		t.Fatalf("got %d %q", code, errout)
	}
	if _, err := os.Stat(path + ".token"); !os.IsNotExist(err) {
		t.Errorf("token file must not exist without --api-listen: %v", err)
	}
}

func TestAPIRefusesUnsafeOrCorruptTokenFile(t *testing.T) {
	for name, prepare := range map[string]func(string){
		"group readable": func(p string) {
			token, _ := remote.GenerateToken()
			_ = remote.SaveToken(p, token)
			_ = os.Chmod(p, 0o640)
		},
		"corrupt": func(p string) { _ = os.WriteFile(p, []byte("garbage"), 0o600) },
	} {
		path := filepath.Join(t.TempDir(), "up.db")
		prepare(path + ".token")
		before, _ := os.ReadFile(path + ".token")
		code, out, errout := runCmd(context.Background(), "serve", "--db", path, "--api-listen", "127.0.0.1:0")
		if code != 1 || out != "" || !strings.HasPrefix(errout, "up: ") || strings.Contains(errout, "access token (shown once)") {
			t.Errorf("%s: got %d %q %q", name, code, out, errout)
		}
		after, _ := os.ReadFile(path + ".token")
		if string(before) != string(after) {
			t.Errorf("%s: token file changed", name)
		}
	}
}

func TestAPIInvalidCertificatePairCreatesNoToken(t *testing.T) {
	clearAPIEnv(t)
	path := filepath.Join(t.TempDir(), "up.db")
	code, out, errout := runCmd(context.Background(), "serve", "--db", path, "--api-listen", "127.0.0.1:0", "--tls-cert", filepath.Join(filepath.Dir(path), "missing-cert"), "--tls-key", filepath.Join(filepath.Dir(path), "missing-key"))
	if code != 1 || out != "" || !strings.Contains(errout, "missing-cert") || strings.Contains(errout, "access token") {
		t.Fatalf("got %d %q %q", code, out, errout)
	}
	if _, err := os.Stat(path + ".token"); !os.IsNotExist(err) {
		t.Errorf("unexpected token: %v", err)
	}
}

func TestAPIListenFailureCreatesNoToken(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	path := filepath.Join(t.TempDir(), "up.db")
	code, _, errout := runCmd(context.Background(), "serve", "--db", path, "--api-listen", busy.Addr().String())
	if code != 1 || strings.Contains(errout, "access token (shown once)") {
		t.Fatalf("got %d %q", code, errout)
	}
	if _, err := os.Stat(path + ".token"); !os.IsNotExist(err) {
		t.Errorf("a failed start must not leave a token behind: %v", err)
	}
}

func TestAPINonloopbackHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "up.db")
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	lines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	done := make(chan int, 1)
	go func() {
		code := run(ctx, []string{"serve", "--db", path, "--api-listen", "0.0.0.0:0"}, io.Discard, writer)
		writer.Close()
		done <- code
	}()
	var warned, listening, monitoring bool
	timeout := time.After(5 * time.Second)
	for !listening || !monitoring {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("serve stopped before listening")
			}
			if line == "up: WARNING: API over plain HTTP; the token and data are not encrypted on the network" {
				warned = true
			}
			if strings.HasPrefix(line, "up: API listening on ") {
				listening = true
			}
			if strings.Contains(line, "monitoring started") {
				monitoring = true
			}
		case <-timeout:
			t.Fatal("serve did not listen")
		}
	}
	if warned {
		t.Error("unexpected plain HTTP warning")
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
	reader.Close()
}

func TestRemovedTransportFlags(t *testing.T) {
	clearAPIEnv(t)
	_, help, _ := runCmd(context.Background(), "serve", "-h")
	for _, name := range []string{"no-tls", "behind-proxy"} {
		for _, suffix := range []string{"", "=false"} {
			dir := t.TempDir()
			code, out, errout := runCmd(context.Background(), "serve", "--db", filepath.Join(dir, "up.db"), "--"+name+suffix)
			want := "up serve: flag provided but not defined: -" + name + "\n\n" + help
			if code != 2 || out != "" || errout != want {
				t.Errorf("%s%s: got %d %q %q", name, suffix, code, out, errout)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("changed files: %v", entries)
			}
		}
	}
}
