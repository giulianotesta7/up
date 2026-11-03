package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/giulianotesta7/up/internal/client"
	"github.com/giulianotesta7/up/internal/connection"
	"github.com/giulianotesta7/up/internal/tui"
)

const loginHelp = `Usage: up login [--url URL]

Connect to a remote up server and save its validated access token.

Flags:
`

var runLoginProgram = tui.RunLogin
var loginIsTerminal = func() bool { return term.IsTerminal(os.Stdin.Fd()) && term.IsTerminal(os.Stdout.Fd()) }

func login(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	u := fs.String("url", "", "prefill the server URL")
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
