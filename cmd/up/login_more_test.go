package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heohlee/up/internal/connection"
	"github.com/heohlee/up/internal/tui"
)

const loginSecret = "up_LOGIN-SECRET-0123456789abcdefghijklmnopqrstu"

func stubLogin(t *testing.T, runner func(context.Context, tui.LoginValidator, ...string) (tui.LoginResult, error)) {
	t.Helper()
	isolateDefaultDatabase(t)
	previousRunner, previousTerminal := runLoginProgram, loginIsTerminal
	runLoginProgram, loginIsTerminal = runner, func() bool { return true }
	t.Cleanup(func() { runLoginProgram, loginIsTerminal = previousRunner, previousTerminal })
}

// The validator handed to the modal in production must be the real one: it
// pings the server with the token and accepts only an up server.
func TestLoginUsesTheRealValidator(t *testing.T) {
	var pings int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pings++
		if r.URL.Path != "/api/v1/ping" || r.Header.Get("Authorization") != "Bearer "+loginSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"name":"up","version":"test"}`))
	}))
	defer server.Close()
	var accepted, rejected error
	stubLogin(t, func(ctx context.Context, validate tui.LoginValidator, _ ...string) (tui.LoginResult, error) {
		accepted = validate(ctx, server.URL, loginSecret)
		rejected = validate(ctx, server.URL, "up_wrong")
		return tui.LoginResult{Connected: true, ServerURL: server.URL, Token: loginSecret}, nil
	})
	code, out, errout := runCmd(context.Background(), "login")
	if code != 0 || accepted != nil || !strings.Contains(rejected.Error(), "token rejected") || pings != 2 {
		t.Fatalf("exit %d accepted=%v rejected=%v pings=%d", code, accepted, rejected, pings)
	}
	if strings.Contains(out+errout, loginSecret) {
		t.Errorf("the token was printed: %q %q", out, errout)
	}
}

func TestLoginOnlySavesWhatTheModalReturned(t *testing.T) {
	stubLogin(t, func(context.Context, tui.LoginValidator, ...string) (tui.LoginResult, error) {
		return tui.LoginResult{ServerURL: "https://typed.example.com", Token: loginSecret}, nil // Connected is false
	})
	code, out, errout := runCmd(context.Background(), "login")
	if code != 1 || out != "" || errout != "up: login cancelled\n" {
		t.Fatalf("got %d %q %q", code, out, errout)
	}
	if _, err := connection.LoadProfile(); err != connection.ErrNoProfile {
		t.Errorf("a login that was not validated saved something: %v", err)
	}
}

func TestLoginPrefillsOnlyWhatWasAsked(t *testing.T) {
	var got []string
	stubLogin(t, func(_ context.Context, _ tui.LoginValidator, prefilled ...string) (tui.LoginResult, error) {
		got = prefilled
		return tui.LoginResult{}, nil
	})
	runCmd(context.Background(), "login")
	if len(got) != 0 && got[0] != "" {
		t.Errorf("no --url must not prefill: %q", got)
	}
	runCmd(context.Background(), "login", "--url", "HTTPS://Up.Acme.com/")
	if len(got) != 1 || got[0] != "https://up.acme.com" {
		t.Errorf("--url must be normalized: %q", got)
	}
}

func TestLogoutTouchesOnlyTheSavedRemote(t *testing.T) {
	isolateDefaultDatabase(t)
	if code, out, errout := runCmd(context.Background(), "logout"); code != 0 || out != "up: no saved remote\n" || errout != "" {
		t.Fatalf("nothing saved: %d %q %q", code, out, errout)
	}
	if err := connection.SaveProfile(connection.Profile{ServerURL: "https://up.acme.com", Token: loginSecret}); err != nil {
		t.Fatal(err)
	}
	path, _ := connection.ProfilePath()
	neighbor := filepath.Join(filepath.Dir(path), "up.db")
	if err := os.WriteFile(neighbor, []byte("database"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errout := runCmd(context.Background(), "logout")
	if code != 0 || out != "up: removed the saved remote\n" || errout != "" || strings.Contains(out+errout, loginSecret) {
		t.Fatalf("logout: %d %q %q", code, out, errout)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("profile still exists: %v", err)
	}
	if data, err := os.ReadFile(neighbor); err != nil || string(data) != "database" {
		t.Errorf("logout touched a neighbor file: %q %v", data, err)
	}
	if code, out, _ := runCmd(context.Background(), "logout"); code != 0 || out != "up: no saved remote\n" {
		t.Errorf("second logout: %d %q", code, out)
	}
	if code, out, errout := runCmd(context.Background(), "logout", "extra"); code != 2 || out != "" || !strings.Contains(errout, "Usage: up logout") {
		t.Errorf("extra argument: %d %q %q", code, out, errout)
	}
}

func TestUsageListsLoginAndLogout(t *testing.T) {
	code, out, _ := runCmd(context.Background(), "-h")
	if code != 0 || !strings.Contains(out, "login ") || !strings.Contains(out, "logout ") {
		t.Errorf("usage: %d %q", code, out)
	}
}
