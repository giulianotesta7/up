package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/config"
	"github.com/giulianotesta7/up/internal/monitor"
	"github.com/giulianotesta7/up/internal/store"
)

const serveHelp = `Usage: up serve [flags]

Runs checks stored in SQLite right away and then at their intervals, storing
results until SIGINT or SIGTERM. New checks are discovered about every 2 seconds
without restarting existing checks. No YAML is required; an empty database is OK.

--config PATH is an optional YAML import before scheduling. New definitions are
appended, identical definitions are unchanged, and any same-name conflict rejects
the entire import. Invalid imports start nothing. Existing definitions, history,
and YAML files are preserved.

Flags:
`

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "optional YAML file to import into SQLite")
	dbPath := fs.String("db", "up.db", "path to the SQLite database (created if missing)")
	if code, ok := parseFlags(fs, serveHelp, args, stdout, stderr); !ok {
		return code
	}

	var checks []config.Check
	if *cfgPath != "" {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintf(stderr, "up: %v\n", err)
			return 1
		}
		checks = cfg.Checks
	}
	s, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	defer s.Close()

	err = monitor.Run(ctx, monitor.Options{
		Checks:            checks,
		Store:             s,
		Runner:            check.New(),
		HeartbeatInterval: monitor.DefaultHeartbeatInterval,
		Logger:            slog.New(slog.NewTextHandler(stderr, nil)),
	})
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	return 0
}
