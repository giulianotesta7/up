package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/heohlee/up/internal/connection"
	"github.com/heohlee/up/internal/tui"
)

const stdinSecret = "up_STDIN-SECRET-0123456789abcdefghijklmnopqrstu"

type stdinFixture struct {
	t      *testing.T
	server *httptest.Server
	runner int
}

// stubStdin isolates the config directory, fakes stdin and records whether the
// interactive modal was started: the stdin mode must never start it.
func stubStdin(t *testing.T, input string, terminal bool) *stdinFixture {
	t.Helper()
	isolateDefaultDatabase(t)
	f := &stdinFixture{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/ping" || r.Header.Get("Authorization") != "Bearer "+stdinSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"name":"up","version":"test"}`))
	}))
	t.Cleanup(f.server.Close)
	prevIn, prevTerm, prevRunner, prevLoginTerm := loginStdin, loginStdinIsTerminal, runLoginProgram, loginIsTerminal
	loginStdin, loginStdinIsTerminal = strings.NewReader(input), func() bool { return terminal }
	runLoginProgram = func(context.Context, tui.LoginValidator, ...string) (tui.LoginResult, error) {
		f.runner++
		return tui.LoginResult{}, nil
	}
	loginIsTerminal = func() bool { return false } // proves the stdin mode needs no terminal
	t.Cleanup(func() {
		loginStdin, loginStdinIsTerminal, runLoginProgram, loginIsTerminal = prevIn, prevTerm, prevRunner, prevLoginTerm
	})
	return f
}

func noProfile(t *testing.T) {
	t.Helper()
	if _, err := connection.LoadProfile(); err != connection.ErrNoProfile {
		t.Errorf("nothing may be saved on failure: %v", err)
	}
}

func TestLoginTokenStdinSavesAValidatedToken(t *testing.T) {
	for _, input := range []string{stdinSecret + "\n", stdinSecret, "  " + stdinSecret + "\r\n"} {
		f := stubStdin(t, input, false)
		code, out, errout := runCmd(context.Background(), "login", "--url", f.server.URL+"/", "--token-stdin")
		path, _ := connection.ProfilePath()
		if code != 0 || out != "up: connected to "+f.server.URL+"; saved to "+path+"\n" || strings.Contains(out+errout, stdinSecret) {
			t.Fatalf("input %q: %d %q %q", input, code, out, errout)
		}
		if !strings.Contains(errout, "WARNING: plain http: the token is sent unencrypted") {
			t.Errorf("a plain http URL must warn on stderr: %q", errout)
		}
		p, err := connection.LoadProfile()
		if err != nil || p.Token != stdinSecret || p.ServerURL != f.server.URL {
			t.Errorf("saved profile: %+v %v", p, err)
		}
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", info.Mode().Perm())
		}
		if f.runner != 0 {
			t.Error("the interactive modal must not start in stdin mode")
		}
	}
}

func TestLoginTokenStdinRejectsBadInputWithoutSavingOrEchoing(t *testing.T) {
	long := strings.Repeat("a", 5000)
	for name, tc := range map[string]struct {
		input    string
		terminal bool
		args     []string
		want     string
	}{
		"no url":         {stdinSecret, false, []string{"--token-stdin"}, "up: --token-stdin requires --url\n"},
		"invalid url":    {stdinSecret, false, []string{"--url", "nope", "--token-stdin"}, "up: enter a full URL such as https://up.example.com\n"},
		"empty":          {"\n", false, nil, "up: no token on stdin\n"},
		"blank":          {"   \r\n", false, nil, "up: no token on stdin\n"},
		"two lines":      {stdinSecret + "\nsecond\n", false, nil, "up: the token on stdin must be a single line\n"},
		"inner space":    {"up_a b", false, nil, "up: the token on stdin must be a single line\n"},
		"too long":       {long, false, nil, "up: the token on stdin is too long\n"},
		"typed terminal": {stdinSecret, true, nil, "up: --token-stdin expects the token piped on stdin\n"},
		"wrong token":    {"up_wrong", false, nil, "up: Token rejected (401). Check it and retry.\n"},
	} {
		f := stubStdin(t, tc.input, tc.terminal)
		args := append([]string{"login"}, tc.args...)
		if tc.args == nil || tc.args[0] != "--url" && tc.args[0] != "--token-stdin" {
			args = append(args, "--url", f.server.URL, "--token-stdin")
		}
		code, out, errout := runCmd(context.Background(), args...)
		stderr := strings.ReplaceAll(errout, "up: WARNING: plain http: the token is sent unencrypted\n", "")
		if code != 1 || out != "" || stderr != tc.want {
			t.Errorf("%s: got %d %q %q, want 1 %q", name, code, out, errout, tc.want)
		}
		for _, leaked := range []string{stdinSecret, "up_wrong", long} {
			if strings.Contains(out+errout, leaked) {
				t.Errorf("%s: output echoes the token", name)
			}
		}
		noProfile(t)
		if f.runner != 0 {
			t.Errorf("%s: the modal started", name)
		}
	}
}

func TestLoginTokenStdinReportsAnUnreachableServer(t *testing.T) {
	f := stubStdin(t, stdinSecret, false)
	address := f.server.URL
	f.server.Close()
	code, out, errout := runCmd(context.Background(), "login", "--url", address, "--token-stdin")
	host := strings.TrimPrefix(address, "http://")
	if code != 1 || out != "" || !strings.Contains(errout, "up: Cannot reach "+host+": connection refused.\n") {
		t.Fatalf("got %d %q %q", code, out, errout)
	}
	noProfile(t)
}

func TestLoginTokenStdinReplacesAnExistingRemote(t *testing.T) {
	f := stubStdin(t, stdinSecret, false)
	if err := connection.SaveProfile(connection.Profile{ServerURL: "https://old.example.com", Token: "old-token"}); err != nil {
		t.Fatal(err)
	}
	if code, _, errout := runCmd(context.Background(), "login", "--url", f.server.URL, "--token-stdin"); code != 0 {
		t.Fatalf("exit %d %q", code, errout)
	}
	if p, _ := connection.LoadProfile(); p.Token != stdinSecret || p.ServerURL != f.server.URL {
		t.Errorf("the old remote was not replaced: %+v", p)
	}
	// A failed login keeps the previous remote.
	stubIn := strings.NewReader("up_wrong")
	loginStdin = stubIn
	if code, _, _ := runCmd(context.Background(), "login", "--url", f.server.URL, "--token-stdin"); code != 1 {
		t.Fatal("wrong token must fail")
	}
	if p, _ := connection.LoadProfile(); p.Token != stdinSecret {
		t.Errorf("a failed login must not touch the saved remote: %+v", p)
	}
}

func TestLoginHelpDocumentsTokenStdin(t *testing.T) {
	code, out, _ := runCmd(context.Background(), "login", "-h")
	if code != 0 || !strings.Contains(out, "--token-stdin") || !strings.Contains(out, "never as an argument") {
		t.Errorf("help: %d %q", code, out)
	}
}
