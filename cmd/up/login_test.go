package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/giulianotesta7/up/internal/client"
	"github.com/giulianotesta7/up/internal/connection"
	"github.com/giulianotesta7/up/internal/tui"
)

func TestLoginAndLogout(t *testing.T) {
	isolateDefaultDatabase(t)
	oldRun, oldTerm := runLoginProgram, loginIsTerminal
	t.Cleanup(func() { runLoginProgram = oldRun; loginIsTerminal = oldTerm })
	loginIsTerminal = func() bool { return true }
	called := false
	runLoginProgram = func(_ context.Context, validate tui.LoginValidator, urls ...string) (tui.LoginResult, error) {
		called = true
		if validate == nil || reflect.ValueOf(validate).Pointer() != reflect.ValueOf(client.Validate).Pointer() || len(urls) != 1 || urls[0] != "https://example.com" {
			t.Fatalf("runner args: %v", urls)
		}
		return tui.LoginResult{Connected: true, ServerURL: "HTTPS://EXAMPLE.COM/", Token: "up_secret"}, nil
	}
	path, _ := connection.ProfilePath()
	code, out, errout := runCmd(context.Background(), "login", "--url", "HTTPS://EXAMPLE.COM/")
	if code != 0 || out != "up: connected to https://example.com; saved to "+path+"\n" || errout != "" || !called {
		t.Fatalf("login: %d %q %q", code, out, errout)
	}
	p, err := connection.LoadProfile()
	if err != nil || p != (connection.Profile{ServerURL: "https://example.com", Token: "up_secret"}) {
		t.Fatalf("profile %+v %v", p, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", info, err)
	}
	other := filepath.Join(filepath.Dir(path), "other")
	if err := os.WriteFile(other, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"up: removed the saved remote\n", "up: no saved remote\n"} {
		code, out, errout = runCmd(context.Background(), "logout")
		if code != 0 || out != want || errout != "" {
			t.Fatalf("logout: %d %q %q", code, out, errout)
		}
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("profile still exists: %v", err)
	}
}

func TestLoginRejectsEmptyExplicitURL(t *testing.T) {
	isolateDefaultDatabase(t)
	oldRun, oldTerm := runLoginProgram, loginIsTerminal
	t.Cleanup(func() { runLoginProgram = oldRun; loginIsTerminal = oldTerm })
	loginIsTerminal = func() bool { return true }
	called := false
	runLoginProgram = func(context.Context, tui.LoginValidator, ...string) (tui.LoginResult, error) {
		called = true
		return tui.LoginResult{}, nil
	}
	code, out, errout := runCmd(context.Background(), "login", "--url", "")
	if code != 1 || out != "" || errout != "up: enter the server URL\n" || called {
		t.Fatalf("empty URL: %d %q %q called=%v", code, out, errout, called)
	}
}

func TestLoginSaveFailureDoesNotExposeToken(t *testing.T) {
	isolateDefaultDatabase(t)
	oldRun, oldTerm := runLoginProgram, loginIsTerminal
	t.Cleanup(func() { runLoginProgram = oldRun; loginIsTerminal = oldTerm })
	loginIsTerminal = func() bool { return true }
	runLoginProgram = func(context.Context, tui.LoginValidator, ...string) (tui.LoginResult, error) {
		return tui.LoginResult{Connected: true, ServerURL: "https://example.com", Token: "up_secret"}, nil
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "up"), []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errout := runCmd(context.Background(), "login")
	if code != 1 || out != "" || strings.Contains(errout, "up_secret") {
		t.Fatalf("save failure: %d %q %q", code, out, errout)
	}
}

func TestLoginFailuresAndHelp(t *testing.T) {
	isolateDefaultDatabase(t)
	oldRun, oldTerm := runLoginProgram, loginIsTerminal
	t.Cleanup(func() { runLoginProgram = oldRun; loginIsTerminal = oldTerm })
	loginIsTerminal = func() bool { return true }
	called := false
	runLoginProgram = func(context.Context, tui.LoginValidator, ...string) (tui.LoginResult, error) {
		called = true
		return tui.LoginResult{}, nil
	}
	code, out, e := runCmd(context.Background(), "login")
	if code != 1 || out != "" || e != "up: login cancelled\n" || !called {
		t.Fatalf("cancel %d %q %q", code, out, e)
	}
	runLoginProgram = func(context.Context, tui.LoginValidator, ...string) (tui.LoginResult, error) {
		return tui.LoginResult{}, errors.New("runner failed")
	}
	code, out, e = runCmd(context.Background(), "login")
	if code != 1 || out != "" || e != "up: runner failed\n" {
		t.Fatalf("runner %d %q %q", code, out, e)
	}
	called = false
	loginIsTerminal = func() bool { return false }
	code, out, e = runCmd(context.Background(), "login")
	if code != 1 || out != "" || e != "up: login needs an interactive terminal\n" || called {
		t.Fatalf("terminal %d %q %q", code, out, e)
	}
	path, _ := connection.ProfilePath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cancel saved file: %v", err)
	}
	for _, tc := range []struct {
		args   []string
		code   int
		stdout bool
	}{{[]string{"login", "-h"}, 0, true}, {[]string{"login", "--bad"}, 2, false}, {[]string{"login", "extra"}, 2, false}, {[]string{"login", "--url", "bad"}, 1, false}} {
		code, out, e = runCmd(context.Background(), tc.args...)
		if code != tc.code || (tc.stdout && (!strings.Contains(out, "Usage: up login") || e != "")) || (!tc.stdout && out != "") {
			t.Fatalf("%v: %d %q %q", tc.args, code, out, e)
		}
	}
}
