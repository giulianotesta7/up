package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/giulianotesta7/up/internal/alert"
	"github.com/giulianotesta7/up/internal/check"
	"github.com/giulianotesta7/up/internal/monitor"
	"github.com/giulianotesta7/up/internal/remote"
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
A webhook channel POSTs a versioned JSON body; a 2xx answer is delivered, anything
else is an error, redirects are not followed and there are no retries.
SMTP servers and channels are managed in up tui. SMTP passwords are encrypted
with a key kept in <db>.key (mode 0600) next to the database; back it up with
the database. Without it stored passwords must be entered again. up serve refuses
a key file readable by group or others.

The optional API is enabled with --api-listen. It serves HTTP without certificates
or HTTPS with --tls-cert and --tls-key together.
The access token hash is stored in <db>.token (0600); a new token is shown once on
stderr. Use up token reset to replace it. GET /api/v1/snapshot returns the current
monitoring snapshot without SMTP passwords.

API and retention flags also read environment variables: UP_API_LISTEN,
UP_TLS_CERT, UP_TLS_KEY and UP_RETENTION. An explicit flag overrides its variable.

Flags:
`

func serve(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dbPath := databaseFlag(fs)
	// Each API flag defaults to its UP_* variable, so containers can be
	// configured with environment alone; an explicit flag always wins.
	apiListen := fs.String("api-listen", os.Getenv("UP_API_LISTEN"), "enable the API on this address (env UP_API_LISTEN)")
	tlsCert := fs.String("tls-cert", os.Getenv("UP_TLS_CERT"), "TLS certificate file, requires --tls-key (env UP_TLS_CERT)")
	tlsKey := fs.String("tls-key", os.Getenv("UP_TLS_KEY"), "TLS private key file, requires --tls-cert (env UP_TLS_KEY)")
	retention := fs.Duration("retention", 720*time.Hour, "how long to keep results, for example 720h (30 days); 0 keeps everything (env UP_RETENTION)")
	if code, ok := parseFlags(fs, serveHelp, args, stdout, stderr); !ok {
		return code
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if value := os.Getenv("UP_RETENTION"); value != "" && !set["retention"] {
		var err error
		*retention, err = time.ParseDuration(value)
		if err != nil {
			fmt.Fprintf(stderr, "up: UP_RETENTION: invalid duration %q\n", value)
			return 1
		}
		if *retention < 0 {
			fmt.Fprintln(stderr, "up: UP_RETENTION must not be negative")
			return 1
		}
	}
	if *retention < 0 {
		fmt.Fprintln(stderr, "up: --retention must not be negative")
		return 1
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		fmt.Fprintln(stderr, "up: --tls-cert and --tls-key must be provided together")
		return 1
	}
	if *apiListen == "" && (*tlsCert != "" || *tlsKey != "") {
		fmt.Fprintln(stderr, "up: --tls-cert and --tls-key require --api-listen")
		return 1
	}
	if *apiListen != "" {
		if err := remote.ValidateListen(*apiListen); err != nil {
			fmt.Fprintf(stderr, "up: %v\n", err)
			return 1
		}
	}
	if err := prepareDatabase(fs, dbPath); err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}

	if *apiListen != "" {
		_, err := remote.LoadVerifier(*dbPath + ".token")
		if err != nil && !errors.Is(err, remote.ErrNoToken) {
			fmt.Fprintf(stderr, "up: %v\n", err)
			return 1
		}
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
	apiCtx, cancelAPI := context.WithCancel(ctx)
	defer cancelAPI()
	var apiDone <-chan error
	if *apiListen != "" {
		var err error
		apiDone, err = startAPI(apiCtx, cancelAPI, *apiListen, *tlsCert, *tlsKey, *dbPath+".token", stderr, s)
		if err != nil {
			fmt.Fprintf(stderr, "up: %v\n", err)
			return 1
		}
		defer func() { cancelAPI(); <-apiDone }()
	}
	ctx = apiCtx
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
	if apiDone != nil {
		cancelAPI()
		if err := <-apiDone; err != nil {
			fmt.Fprintf(stderr, "up: %v\n", err)
			return 1
		}
	}
	return 0
}

// envBool reads a boolean variable; unset or empty means false.
func envBool(name string) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s: invalid value %q (use true or false)", name, value)
	}
	return b, nil
}
