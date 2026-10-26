package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/giulianotesta7/up/internal/store"
	"github.com/giulianotesta7/up/internal/tui"
)

const tuiHelp = `Usage: up tui [flags]

Shows the checks and results stored by 'up serve', refreshing every 2s.
It only reads the database; checks keep running while it is closed.

Keys: ↑/↓ or k/j select · enter details · esc back · q quit

Flags:
`

func runTUI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	dbPath := fs.String("db", "up.db", "path to the SQLite database written by 'up serve'")
	if code, ok := parseFlags(fs, tuiHelp, args, stdout, stderr); !ok {
		return code
	}

	s, err := store.OpenReadOnly(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	defer s.Close()

	if err := tui.Run(ctx, s); err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	return 0
}
