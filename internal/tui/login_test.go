package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/giulianotesta7/up/internal/connection"
)

type loginCall struct{ url, token string }

func newTestLogin(validate LoginValidator) loginModel {
	m := NewLogin(validate)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return next.(loginModel)
}

func loginType(m loginModel, text string) loginModel {
	next, _ := m.Update(tea.PasteMsg{Content: text})
	return next.(loginModel)
}

func loginPress(m loginModel, key string) (loginModel, tea.Cmd) {
	var msg tea.KeyPressMsg
	switch key {
	case "tab":
		msg = tea.KeyPressMsg{Code: tea.KeyTab}
	case "enter":
		msg = tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		msg = tea.KeyPressMsg{Code: tea.KeyBackspace}
	default:
		msg = tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
	next, cmd := m.Update(msg)
	return next.(loginModel), cmd
}

func loginPlain(m loginModel) string { return ansi.Strip(m.View().Content) }

func TestLoginPrefilledURL(t *testing.T) {
	m := NewLogin(nil, "https://example.com")
	if !strings.Contains(loginPlain(m), "https://example.com") {
		t.Fatal("prefill missing")
	}
	m = loginType(m, "/path")
	if m.values[loginFieldURL] != "https://example.com/path" {
		t.Fatalf("url = %q", m.values[loginFieldURL])
	}
}

func TestLoginEmptyShowsModal(t *testing.T) {
	m := newTestLogin(nil)
	view := loginPlain(m)
	for _, want := range []string{"Connect to a remote up", "Server URL", "Access token", "paste your token", "enter connect", "esc cancel", "not connected"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestLoginMasksToken(t *testing.T) {
	m := newTestLogin(nil)
	m = loginType(m, "https://up.acme.com")
	m, _ = loginPress(m, "tab")
	m = loginType(m, "s3cret-token")
	view := loginPlain(m)
	if strings.Contains(view, "s3cret-token") {
		t.Fatalf("token visible:\n%s", view)
	}
	if !strings.Contains(view, strings.Repeat("•", len("s3cret-token"))) {
		t.Errorf("token not masked:\n%s", view)
	}
	if !strings.Contains(view, "https://up.acme.com") {
		t.Errorf("url should stay visible:\n%s", view)
	}
}

func TestLoginAcceptsPlainHTTPWithAWarning(t *testing.T) {
	m := newTestLogin(func(context.Context, string, string) error { return nil })
	if strings.Contains(loginPlain(m), "unencrypted") {
		t.Fatal("no warning expected for an empty URL")
	}
	m = loginType(m, "http://100.64.0.5:8443")
	view := loginPlain(m)
	if !strings.Contains(view, "⚠ Plain http: the token is sent unencrypted") {
		t.Errorf("missing http warning:\n%s", view)
	}
}

func TestLoginHTTPSShowsNoWarning(t *testing.T) {
	m := newTestLogin(nil)
	m = loginType(m, "https://up.acme.com")
	if strings.Contains(loginPlain(m), "unencrypted") {
		t.Error("https must not warn")
	}
}

func TestLoginSubmitsPlainHTTPToTheValidator(t *testing.T) {
	var got loginCall
	m := newTestLogin(func(_ context.Context, u, tok string) error { got = loginCall{u, tok}; return nil })
	m = loginFill(m, "http://100.64.0.5:8443/", "tok")
	m, cmd := loginSubmit(t, m)
	next, quit := m.Update(loginDone(cmd))
	m = next.(loginModel)
	if got != (loginCall{"http://100.64.0.5:8443", "tok"}) || !m.Result().Connected || quit == nil {
		t.Fatalf("validator got %+v, result %+v", got, m.Result())
	}
}

func TestLoginRequiresToken(t *testing.T) {
	m := newTestLogin(func(context.Context, string, string) error { t.Fatal("validator called"); return nil })
	m = loginType(m, "https://up.acme.com")
	m, cmd := loginPress(m, "enter")
	if cmd != nil {
		t.Fatal("no command expected without a token")
	}
	if view := loginPlain(m); !strings.Contains(view, "Enter the access token.") {
		t.Errorf("missing token error:\n%s", view)
	}
}

// loginSubmit drives enter and delivers the validator result back to the model.
func loginSubmit(t *testing.T, m loginModel) (loginModel, tea.Cmd) {
	t.Helper()
	m, cmd := loginPress(m, "enter")
	if cmd == nil {
		t.Fatal("expected a validation command")
	}
	return m, cmd
}

// loginDone runs a command batch and returns the validation result message,
// ignoring the spinner tick that travels in the same batch.
func loginDone(cmd tea.Cmd) tea.Msg {
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return msg
	}
	out := make(chan tea.Msg, len(batch))
	for _, c := range batch {
		go func() {
			if m := c(); m != nil {
				if _, isDone := m.(loginDoneMsg); isDone {
					out <- m
				}
			}
		}()
	}
	return <-out
}

func loginFill(m loginModel, url, token string) loginModel {
	m = loginType(m, url)
	m, _ = loginPress(m, "tab")
	return loginType(m, token)
}

func TestLoginValidatingLocksFieldsAndShowsProgress(t *testing.T) {
	block := make(chan struct{})
	var got loginCall
	m := newTestLogin(func(_ context.Context, u, tok string) error { got = loginCall{u, tok}; <-block; return nil })
	m = loginFill(m, "https://up.acme.com/", "tok")
	m, cmd := loginSubmit(t, m)
	view := loginPlain(m)
	if !strings.Contains(view, "Checking certificate and token…") || !strings.Contains(view, "esc cancel") {
		t.Errorf("missing progress:\n%s", view)
	}
	before := loginPlain(m)
	m = loginType(m, "zzz")
	if loginPlain(m) != before {
		t.Error("fields must be locked while validating")
	}
	close(block)
	msg := loginDone(cmd)
	if got != (loginCall{"https://up.acme.com", "tok"}) {
		t.Errorf("validator got %+v", got)
	}
	next, quit := m.Update(msg)
	m = next.(loginModel)
	res := m.Result()
	if !res.Connected || res.ServerURL != "https://up.acme.com" || res.Token != "tok" {
		t.Fatalf("result = %+v", res)
	}
	if quit == nil {
		t.Fatal("success must quit")
	}
}

func TestLoginErrorsUnlockAndSaveNothing(t *testing.T) {
	cases := map[error]string{
		connection.ErrTokenRejected:  "Token rejected (401). Check it and retry.",
		connection.ErrCertUntrusted:  "Certificate not trusted (unknown authority).",
		connection.ErrUnreachable:    "Cannot reach up.acme.com: connection timed out.",
		errors.New("server says no"): "Server says no.",
	}
	for verr, want := range cases {
		err := verr
		if errors.Is(err, connection.ErrUnreachable) {
			err = fmt.Errorf("%w: connection timed out", connection.ErrUnreachable)
		}
		m := newTestLogin(func(context.Context, string, string) error { return err })
		m = loginFill(m, "https://up.acme.com", "tok")
		m, cmd := loginSubmit(t, m)
		next, quit := m.Update(loginDone(cmd))
		m = next.(loginModel)
		if m.Result().Connected || quit != nil {
			t.Errorf("%v: failure must not connect or quit", verr)
		}
		view := loginPlain(m)
		if !strings.Contains(view, want) {
			t.Errorf("%v: missing %q:\n%s", verr, want, view)
		}
		if strings.Contains(view, "Checking") {
			t.Errorf("%v: still shows progress", verr)
		}
		m = loginType(m, "x") // fields unlocked again
		if !strings.Contains(loginPlain(m), "tok") && !strings.Contains(loginPlain(m), "••••") {
			t.Errorf("%v: expected editable fields", verr)
		}
	}
}

func TestLoginEscCancelsValidationThenQuits(t *testing.T) {
	var ctxErr error
	done := make(chan struct{})
	m := newTestLogin(func(ctx context.Context, _, _ string) error {
		<-ctx.Done()
		ctxErr = ctx.Err()
		close(done)
		return ctx.Err()
	})
	m = loginFill(m, "https://up.acme.com", "tok")
	m, cmd := loginSubmit(t, m)
	go func() { loginDone(cmd) }()
	m, quit := loginPress(m, "esc")
	<-done
	if !errors.Is(ctxErr, context.Canceled) || quit != nil {
		t.Fatalf("esc while validating must cancel the request only (ctx=%v quit=%v)", ctxErr, quit != nil)
	}
	if strings.Contains(loginPlain(m), "Checking") {
		t.Error("progress should be gone after cancel")
	}
	m, quit = loginPress(m, "esc")
	if quit == nil || m.Result().Connected {
		t.Error("esc when idle must quit without a result")
	}
}

func TestLoginStaleResultIgnored(t *testing.T) {
	m := newTestLogin(func(context.Context, string, string) error { return nil })
	m = loginFill(m, "https://up.acme.com", "tok")
	m, _ = loginSubmit(t, m)
	m, _ = loginPress(m, "esc") // cancel; the request's late result must not connect
	next, quit := m.Update(loginDoneMsg{generation: 1})
	m = next.(loginModel)
	if m.Result().Connected || quit != nil {
		t.Error("late result after cancel must be ignored")
	}
}

func TestLoginCertificateErrorSaysWhatToDo(t *testing.T) {
	m := newTestLogin(func(context.Context, string, string) error { return connection.ErrCertUntrusted })
	m = loginFill(m, "https://up.acme.com", "tok")
	m, cmd := loginSubmit(t, m)
	next, _ := m.Update(loginDone(cmd))
	view := loginPlain(next.(loginModel))
	for _, want := range []string{"✗ Certificate not trusted (unknown authority).", "trusted CA", "system trust store"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
	if next.(loginModel).Result().Connected {
		t.Error("an untrusted certificate must not connect")
	}
}
