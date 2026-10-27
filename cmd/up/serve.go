package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/giulianotesta7/up/internal/alert"
	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/monitor"
	"github.com/giulianotesta7/up/internal/secret"
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

Alerts are sent when a monitor with a channel goes DOWN and when it recovers.
SMTP servers and channels are managed in up tui. SMTP passwords are encrypted
with a key kept in <db>.key (mode 0600) next to the database; back it up with
the database. Without it stored passwords must be entered again. up serve refuses
a key file readable by group or others.

Flags:
`

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dbPath := databaseFlag(fs)
	retention := fs.Duration("retention", 720*time.Hour, "how long to keep results, for example 720h (30 days); 0 keeps everything")
	if code, ok := parseFlags(fs, serveHelp, args, stdout, stderr); !ok {
		return code
	}
	if *retention < 0 {
		fmt.Fprintln(stderr, "up: --retention must not be negative")
		return 1
	}

	if err := prepareDatabase(fs, dbPath); err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	keyPath := *dbPath + ".key"
	var key *secret.Key
	loadKey := func() bool {
		k, created, err := secret.LoadOrCreate(keyPath)
		if err != nil {
			fmt.Fprintf(stderr, "up: %v\n", err)
			return false
		}
		if created {
			logger.Info("created encryption key", "path", keyPath)
		}
		key = k
		return true
	}
	// Check an existing key before opening the database, so a refused start
	// never migrates or otherwise changes it. A missing key is created after
	// the database opens, so database errors keep coming first.
	if _, err := os.Lstat(keyPath); err == nil && !loadKey() {
		return 1
	}
	s, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	defer s.Close()
	if key == nil && !loadKey() {
		return 1
	}
	publicKey, _, err := s.PublicKey(ctx)
	if err == nil && !bytes.Equal(publicKey, key.PublicKey()) {
		err = s.SetPublicKey(ctx, key.PublicKey())
	}
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	hostname, _ := os.Hostname()
	notifier := alert.NewNotifier(alert.Options{Store: s, Key: key, Sender: alert.SMTPSender{}, Hostname: hostname, Logger: logger})
	notifyCtx, cancelNotify := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); notifier.Run(notifyCtx) }()
	err = monitor.Run(ctx, monitor.Options{
		Alerter:           notifier,
		Store:             s,
		Runner:            check.New(),
		HeartbeatInterval: monitor.DefaultHeartbeatInterval,
		Retention:         *retention,
		Logger:            logger,
	})
	cancelNotify()
	<-done
	if err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	return 0
}
