package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/monitor"
	"github.com/giulianotesta7/up/internal/store"
)

const serveHelp = `Usage: up serve [flags]

Runs checks stored in SQLite right away and then at their intervals, storing
results until SIGINT or SIGTERM. New checks are discovered about every 2 seconds
without restarting existing checks. An empty database is OK: add monitors with
"up tui".

Results older than --retention are removed at startup and then hourly. Each
check keeps its newest result older than the cutoff, so a current outage keeps
its start. Monitor definitions are never removed.

Flags:
`

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dbPath := fs.String("db", "up.db", "path to the SQLite database (created if missing)")
	retention := fs.Duration("retention", 720*time.Hour, "how long to keep results, for example 720h (30 days); 0 keeps everything")
	if code, ok := parseFlags(fs, serveHelp, args, stdout, stderr); !ok {
		return code
	}
	if *retention < 0 {
		fmt.Fprintln(stderr, "up: --retention must not be negative")
		return 1
	}

	s, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	defer s.Close()

	err = monitor.Run(ctx, monitor.Options{
		Store:             s,
		Runner:            check.New(),
		HeartbeatInterval: monitor.DefaultHeartbeatInterval,
		Retention:         *retention,
		Logger:            slog.New(slog.NewTextHandler(stderr, nil)),
	})
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	return 0
}
