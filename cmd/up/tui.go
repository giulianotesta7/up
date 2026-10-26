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

Create monitors and show stored results, refreshing every 2s.
Creates the SQLite database on first use; no daemon startup required.
Keep 'up serve' running to discover monitors and record results while closed.
New monitors require a name and HTTP/HTTPS URL. Defaults: interval 30s,
timeout 5s, expected status 200. Creation only; no editing or deletion.

Keys: ↑/↓ or k/j select · enter details · n new monitor · esc back · q/ctrl+c quit
Form: tab/shift+tab focus · enter save · esc cancel · ctrl+c quit

Flags:
`

// runTUIProgram is the interactive boundary, replaced in CLI tests.
var runTUIProgram = tui.Run

func runTUI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	dbPath := fs.String("db", "up.db", "path to the SQLite database (created if missing)")
	if code, ok := parseFlags(fs, tuiHelp, args, stdout, stderr); !ok {
		return code
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	defer s.Close()

	if err := runTUIProgram(ctx, s); err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	return 0
}
