package tui

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/heohlee/up/internal/connection"
)

// LoginValidator checks the token against the server. It must wrap the
// connection.Err* kinds so the modal can explain what failed.
type LoginValidator func(ctx context.Context, serverURL, token string) error

// LoginResult is what the modal produced. Saving is the caller's job and only
// happens when Connected is true, so nothing is stored for a rejected token.
type LoginResult struct {
	Connected bool
	ServerURL string
	Token     string
}

const (
	loginFieldURL = iota
	loginFieldToken
	loginFields
)

var loginSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type loginModel struct {
	validate      LoginValidator
	values        [loginFields]string
	focus, cursor int
	width, height int
	err           string
	validating    bool
	frame         int
	generation    uint64
	cancel        context.CancelFunc
	pendingURL    string
	result        LoginResult
}

type loginDoneMsg struct {
	generation uint64
	err        error
}
type loginTickMsg struct{ generation uint64 }

// NewLogin builds the standalone login modal used by `up login`.
func NewLogin(validate LoginValidator, prefilled ...string) loginModel {
	m := loginModel{validate: validate}
	if len(prefilled) > 0 {
		m.values[loginFieldURL] = prefilled[0]
		m.cursor = len([]rune(prefilled[0]))
	}
	return m
}

// Result reports the outcome after the program ends.
func (m loginModel) Result() LoginResult { return m.result }

func (m loginModel) Init() tea.Cmd { return nil }

func (m loginModel) editor() monitorForm {
	f := monitorForm{cursor: m.cursor}
	f.values[0] = m.values[m.focus]
	return f
}

func (m loginModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.PasteMsg:
		if m.validating {
			return m, nil
		}
		f := m.editor()
		f.insert(msg.Content)
		m.values[m.focus], m.cursor = f.values[0], f.cursor
		m.err = ""
	case loginTickMsg:
		if m.validating && msg.generation == m.generation {
			m.frame = (m.frame + 1) % len(loginSpinner)
			return m, loginTick(m.generation)
		}
	case loginDoneMsg:
		if !m.validating || msg.generation != m.generation {
			return m, nil // late result of a cancelled attempt
		}
		m.validating = false
		m.cancel = nil
		if msg.err != nil {
			m.err = describeLoginError(msg.err, m.pendingURL)
			return m, nil
		}
		m.result = LoginResult{Connected: true, ServerURL: m.pendingURL, Token: m.values[loginFieldToken]}
		return m, tea.Quit
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m loginModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	case "esc":
		if m.validating {
			m.cancel()
			m.cancel, m.validating = nil, false
			return m, nil
		}
		return m, tea.Quit
	}
	if m.validating {
		return m, nil
	}
	switch msg.String() {
	case "tab", "shift+tab":
		delta := 1
		if msg.String() == "shift+tab" {
			delta = -1
		}
		m.focus = (m.focus + delta + loginFields) % loginFields
		m.cursor = len([]rune(m.values[m.focus]))
	case "enter":
		return m.submit()
	default:
		f := m.editor()
		f.edit(msg)
		m.values[m.focus], m.cursor = f.values[0], f.cursor
		m.err = ""
	}
	return m, nil
}

func (m loginModel) submit() (tea.Model, tea.Cmd) {
	serverURL, err := connection.NormalizeURL(m.values[loginFieldURL])
	if err != nil {
		m.err = describeLoginError(err, "")
		m.focus, m.cursor = loginFieldURL, len([]rune(m.values[loginFieldURL]))
		return m, nil
	}
	token := strings.TrimSpace(m.values[loginFieldToken])
	if token == "" {
		m.err = "Enter the access token."
		m.focus, m.cursor = loginFieldToken, 0
		return m, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel, m.validating, m.err, m.frame = cancel, true, "", 0
	m.pendingURL = serverURL
	m.values[loginFieldToken] = token
	m.generation++
	generation, validate := m.generation, m.validate
	check := func() tea.Msg {
		if validate == nil {
			return loginDoneMsg{generation: generation, err: errors.New("no server validation is available")}
		}
		return loginDoneMsg{generation: generation, err: validate(ctx, serverURL, token)}
	}
	return m, tea.Batch(check, loginTick(generation))
}

func loginTick(generation uint64) tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return loginTickMsg{generation} })
}

// DescribeLoginError gives the one-line explanation the login modal shows for
// a failed connection, so non-interactive commands print the same text.
func DescribeLoginError(err error, serverURL string) string {
	return describeLoginError(err, serverURL)
}

// describeLoginError turns a failure kind into one actionable line.
func describeLoginError(err error, serverURL string) string {
	switch {
	case errors.Is(err, connection.ErrTokenRejected):
		return "Token rejected (401). Check it and retry."
	case errors.Is(err, connection.ErrCertUntrusted):
		return "Certificate not trusted (unknown authority).\nUse a certificate from a trusted CA, or add this one to your system trust store."
	case errors.Is(err, connection.ErrUnreachable):
		detail := strings.TrimPrefix(err.Error(), connection.ErrUnreachable.Error())
		detail = strings.TrimSpace(strings.TrimPrefix(detail, ":"))
		host := serverURL
		if u, perr := url.Parse(serverURL); perr == nil && u.Host != "" {
			host = u.Host
		}
		if detail == "" {
			return "Cannot reach " + host + "."
		}
		return "Cannot reach " + host + ": " + strings.TrimRight(detail, ".") + "."
	}
	text := err.Error()
	if text == "" {
		return "Connection failed."
	}
	return strings.ToUpper(text[:1]) + strings.TrimSuffix(text[1:], ".") + "."
}

func (m loginModel) renderBody() string {
	var b strings.Builder
	field := func(i int, label, placeholder string, mask bool) {
		value := m.values[i]
		shown := value
		if mask {
			shown = strings.Repeat("•", len([]rune(value)))
		}
		switch {
		case m.validating || i != m.focus:
			b.WriteString("  " + label + "\n")
			if value == "" {
				b.WriteString("    " + helpStyle.Render(placeholder) + "\n")
			} else {
				b.WriteString("    " + helpStyle.Render(shown) + "\n")
			}
		default:
			runes := []rune(shown)
			b.WriteString("  " + accentStyle.Render(label) + "\n")
			b.WriteString("  " + accentStyle.Render(">") + " " + string(runes[:m.cursor]) + accentStyle.Render("▏") + string(runes[m.cursor:]) + "\n")
		}
	}
	b.WriteString("\n")
	field(loginFieldURL, "Server URL", "https://up.example.com", false)
	b.WriteString("\n")
	field(loginFieldToken, "Access token", "paste your token", true)
	b.WriteString("\n")
	switch {
	case m.validating:
		b.WriteString("  " + accentStyle.Render(loginSpinner[m.frame]) + " Checking certificate and token…\n")
		b.WriteString("  " + renderHelp("esc cancel"))
	default:
		if connection.IsPlainHTTP(m.values[loginFieldURL]) {
			b.WriteString("  " + warnStyle.Render("⚠ Plain http: the token is sent unencrypted") + "\n")
		}
		if m.err != "" {
			first, hint, _ := strings.Cut(m.err, "\n")
			b.WriteString("  " + downStyle.Render("✗ "+first) + "\n")
			if hint != "" {
				b.WriteString("    " + helpStyle.Render(hint) + "\n")
			}
		}
		b.WriteString("  " + renderHelp("tab focus · enter connect · esc cancel"))
	}
	return b.String()
}

const loginModalWidth = 50

func (m loginModel) View() tea.View {
	background := titleStyle.Render(logoMark+" "+productName) + "  " + helpStyle.Render("not connected")
	body := m.renderBody()
	modal, ok := renderModal("Connect to a remote up", body, m.width, m.height, loginModalWidth)
	var content string
	if ok {
		content = overlayPopup(padForPopup(background, m.height), modal, m.width, m.height)
	} else {
		// Small terminals: the same form without the box.
		content = background + "\n" + body
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// RunLogin shows the login modal and returns what the user entered once the
// server accepted the token. A cancelled modal returns a zero result.
func RunLogin(ctx context.Context, validate LoginValidator, prefilled ...string) (LoginResult, error) {
	p := tea.NewProgram(NewLogin(validate, prefilled...), tea.WithContext(ctx))
	final, err := p.Run()
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, tea.ErrProgramKilled) {
			return LoginResult{}, nil
		}
		return LoginResult{}, err
	}
	return final.(loginModel).Result(), nil
}
