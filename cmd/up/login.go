package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/giulianotesta7/up/internal/client"
	"github.com/giulianotesta7/up/internal/connection"
	"github.com/giulianotesta7/up/internal/tui"
)

const loginHelp = `Usage: up login [--url URL]

Connect to a remote up server and save its validated access token.

Without flags it opens an interactive modal. For scripts, pipe the token with
--token-stdin (it requires --url) and never as an argument, so it stays out of the
shell history and the process list:

  echo "$UP_TOKEN" | up login --url https://up.example.com --token-stdin

The token is checked against the server first and nothing is saved if it fails.

Flags:
`

var runLoginProgram = tui.RunLogin

// loginStdin and loginStdinIsTerminal are replaced in tests.
var loginStdin io.Reader = os.Stdin
var loginStdinIsTerminal = func() bool { return term.IsTerminal(os.Stdin.Fd()) }

const maxStdinToken = 4096

var loginIsTerminal = func() bool { return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()) }

func login(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	u := fs.String("url", "", "server URL (prefilled in the modal)")
	tokenStdin := fs.Bool("token-stdin", false, "read the access token from stdin instead of opening the modal (requires --url)")
	if code, ok := parseFlags(fs, loginHelp, args, stdout, stderr); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprint(stderr, loginHelp)
		return 2
	}
	normalized := ""
	urlSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "url" {
			urlSet = true
		}
	})
	if urlSet {
		var err error
		normalized, err = connection.NormalizeURL(*u)
		if err != nil {
			fmt.Fprintf(stderr, "up: %v\n", err)
			return 1
		}
	}
	if *tokenStdin {
		return loginFromStdin(ctx, urlSet, normalized, stdout, stderr)
	}
	if !loginIsTerminal() {
		fmt.Fprintln(stderr, "up: login needs an interactive terminal")
		return 1
	}
	result, err := runLoginProgram(ctx, client.Validate, normalized)
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	if !result.Connected {
		fmt.Fprintln(stderr, "up: login cancelled")
		return 1
	}
	p := connection.Profile{ServerURL: result.ServerURL, Token: result.Token}
	if err = connection.SaveProfile(p); err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	path, err := connection.ProfilePath()
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	url, _ := connection.NormalizeURL(p.ServerURL)
	fmt.Fprintf(stdout, "up: connected to %s; saved to %s\n", url, path)
	return 0
}

// loginFromStdin is the non-interactive login: the same validation and the
// same saved remote as the modal, with the token read from piped stdin.
func loginFromStdin(ctx context.Context, urlSet bool, serverURL string, stdout, stderr io.Writer) int {
	if !urlSet {
		fmt.Fprintln(stderr, "up: --token-stdin requires --url")
		return 1
	}
	if loginStdinIsTerminal() {
		fmt.Fprintln(stderr, "up: --token-stdin expects the token piped on stdin")
		return 1
	}
	data, err := io.ReadAll(io.LimitReader(loginStdin, maxStdinToken+1))
	if err != nil {
		fmt.Fprintln(stderr, "up: cannot read the token from stdin")
		return 1
	}
	if len(data) > maxStdinToken {
		fmt.Fprintln(stderr, "up: the token on stdin is too long")
		return 1
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		fmt.Fprintln(stderr, "up: no token on stdin")
		return 1
	}
	if strings.ContainsAny(token, " \t\r\n") {
		fmt.Fprintln(stderr, "up: the token on stdin must be a single line")
		return 1
	}
	if connection.IsPlainHTTP(serverURL) {
		fmt.Fprintln(stderr, "up: WARNING: plain http: the token is sent unencrypted")
	}
	if err := client.Validate(ctx, serverURL, token); err != nil {
		fmt.Fprintf(stderr, "up: %s\n", tui.DescribeLoginError(err, serverURL))
		return 1
	}
	if err := connection.SaveProfile(connection.Profile{ServerURL: serverURL, Token: token}); err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	path, err := connection.ProfilePath()
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "up: connected to %s; saved to %s\n", serverURL, path)
	return 0
}
