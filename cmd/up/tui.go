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

Create, edit and delete monitors and show stored results, refreshing every 2s.
Creates the SQLite database on first use; no daemon startup required.
Keep 'up serve' running to discover monitors and record results while closed.
New monitors require a name and HTTP/HTTPS URL. Defaults: interval 30s,
timeout 5s, expected status 200. An edit cannot rename a monitor;
a confirmed delete removes history and cannot be undone.

Keys: ↑/↓ or k/j select · enter details · n new monitor · e edit · d delete · a toggle animation · esc back · q/ctrl+c quit
Form: tab/shift+tab focus · enter save · esc cancel · ctrl+c quit
Delete: y delete · n cancel · esc cancel · ctrl+c quit

Flags:
`

// runTUIProgram is the interactive boundary, replaced in CLI tests.
var runTUIProgram = tui.Run

func runTUI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	dbPath := databaseFlag(fs)
	if code, ok := parseFlags(fs, tuiHelp, args, stdout, stderr); !ok {
		return code
	}

	if err := prepareDatabase(fs, dbPath); err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	defer s.Close()

	if err := runTUIProgram(ctx, s, tui.Options{DatabasePath: *dbPath}); err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	return 0
}
