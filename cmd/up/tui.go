package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/giulianotesta7/up/internal/client"
	"github.com/giulianotesta7/up/internal/connection"
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
Forms and confirmations open as popups when they fit; small terminals use full screens.
Manage SMTP servers and channels for email alerts with s and c; choose a monitor's channel in its form.
SMTP passwords are encrypted with the key that 'up serve' keeps in <db>.key; run it once before entering a password, and back up that file with the database.
Channels are email or webhook (a generic JSON POST, not a native Slack or Discord format); webhook URLs and auth values use the same key and are never shown again.

Keys: ↑/↓ or k/j select · enter details (small terminals) · n new monitor · e edit · d delete · s SMTP servers · c channels · a toggle animation · ? all keys · esc back · q/ctrl+c quit
Terminals of at least 110x30 show the selected monitor's detail beside the list, with an activity feed below; enter is not needed there.
Form: tab/shift+tab focus · enter save · esc cancel · ctrl+c quit
Delete: y delete · n cancel · esc cancel · ctrl+c quit

Use --remote or UP_REMOTE=true to connect to the remote saved by 'up login'.
In remote mode, r retry reloads immediately.

Flags:
`

// runTUIProgram is the interactive boundary, replaced in CLI tests.
var runTUIProgram = tui.Run
var runRemoteTUIProgram = tui.RunStore

func runTUI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	dbPath := databaseFlag(fs)
	remoteDefault, remoteErr := envBool("UP_REMOTE")
	remote := fs.Bool("remote", remoteDefault, "connect to the saved remote (env UP_REMOTE)")
	if code, ok := parseFlags(fs, tuiHelp, args, stdout, stderr); !ok {
		return code
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if remoteErr != nil && !set["remote"] {
		fmt.Fprintf(stderr, "up: %v\n", remoteErr)
		return 1
	}
	if *remote {
		if set["db"] {
			fmt.Fprintln(stderr, "up: --db cannot be used with --remote")
			return 1
		}
		profile, err := connection.LoadProfile()
		if errors.Is(err, connection.ErrNoProfile) {
			fmt.Fprintln(stderr, "up: no saved remote; run 'up login'")
			return 1
		}
		if err != nil {
			fmt.Fprintf(stderr, "up: %v\n", err)
			return 1
		}
		u, _ := url.Parse(profile.ServerURL)
		if err := runRemoteTUIProgram(ctx, client.New(profile.ServerURL, profile.Token), tui.Options{Destination: profile.ServerURL, Hostname: u.Host}); err != nil {
			fmt.Fprintf(stderr, "up: %v\n", err)
			return 1
		}
		return 0
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

	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	if err := runTUIProgram(ctx, s, tui.Options{DatabasePath: *dbPath, Hostname: hostname}); err != nil {
		fmt.Fprintf(stderr, "up: %v\n", err)
		return 1
	}
	return 0
}
