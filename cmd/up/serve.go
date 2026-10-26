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

Runs every check in the config file right away and then at its interval,
storing each result in the database, until it receives SIGINT or SIGTERM.
An invalid config is reported and nothing is started.

Flags:
`

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "up.yaml", "path to the YAML config file")
	dbPath := fs.String("db", "up.db", "path to the SQLite database (created if missing)")
	if code, ok := parseFlags(fs, serveHelp, args, stdout, stderr); !ok {
		return code
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	s, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	defer s.Close()

	err = monitor.Run(ctx, monitor.Options{
		Checks:            cfg.Checks,
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
