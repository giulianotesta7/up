<div align="center">

# up

A self-hosted HTTP uptime monitor with a terminal UI.

[![Go quality](https://github.com/giulianotesta7/up/actions/workflows/quality.yml/badge.svg)](https://github.com/giulianotesta7/up/actions/workflows/quality.yml)
[![Go security](https://github.com/giulianotesta7/up/actions/workflows/security.yml/badge.svg)](https://github.com/giulianotesta7/up/actions/workflows/security.yml)

</div>

Uptime monitoring that lives in your terminal.
A single Go binary that never stops watching. Close the TUI, walk away, SSH back in hours later, and catch up on everything you missed. No database server, no agents, no YAML.

## Capabilities

- **HTTP checks.** Each monitor has a URL, an interval, a timeout and an expected status
  code. It is UP when the response status equals the expected one within the timeout;
  otherwise it is DOWN with the reason (unexpected status, timeout, connection error).
  Redirects are followed and the final status is compared.
- **Create monitors in the TUI.** Press `n`, fill in the form, save. The running daemon picks
  the new monitor up within about two seconds, without a restart.
- **Status at a glance.** Status, latency, last check and failure reason for every service.
  Open one to see its last error, when it went down, its recent checks and its recent
  outages (`03:12 → 03:40`).
- **History while you were away.** Every result is stored with its time, status, latency and
  error, and outages are derived from it.
- **Stale-daemon detection.** The daemon writes a heartbeat. If it stops, the TUI says so
  and keeps the last known statuses; it never marks services DOWN just because the data is
  old.
- **Retention.** Results older than 30 days are removed automatically (`--retention` changes
  it, `0` keeps everything). Monitor definitions are never removed.

## Install

On Linux (amd64 or arm64):

```console
curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh
```

The script downloads the release binary, verifies its checksum and installs it in
`/usr/local/bin`. To also run it as a systemd service:

```console
curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh -s -- --service
```

That creates the `up` user, installs the unit from [`deploy/up.service`](deploy/up.service)
and starts it, storing the database in `/var/lib/up/up.db`. Logs are in
`journalctl -u up -f`. Other options: `--version vX.Y.Z` to pin a release and `--uninstall`
to remove the binary and the unit (the database is kept).

To build from source you need Go 1.26.10:

```console
git clone https://github.com/giulianotesta7/up.git
cd up
go build -o up ./cmd/up
```

## Quick start

Start the daemon; an empty database is fine:

```console
up serve
```

In another terminal, open the TUI and press `n` to add your first monitor:

```console
up tui
```

Both commands use `up.db` in the current directory unless you pass `--db PATH`. With the
systemd service, open the TUI on the server with:

```console
sudo -u up up tui --db /var/lib/up/up.db
```

### Keys

| Where | Keys |
| --- | --- |
| List | `↑`/`↓` or `k`/`j` select · `enter` details · `n` new monitor · `q` quit |
| Details | `esc` back · `n` new monitor · `q` quit |
| New monitor | `tab`/`shift+tab` change field · `enter` save · `esc` cancel |

A new monitor needs a name and an HTTP or HTTPS URL. The interval defaults to `30s`, the
timeout to `5s` and the expected status to `200`. The timeout must be greater than zero and
not longer than the interval.

## Commands

| Command | What it does |
| --- | --- |
| `up serve` | Runs the checks and stores the results until SIGINT or SIGTERM. |
| `up tui` | Shows the stored results and creates monitors. Refreshes every 2 seconds. |

| Flag | Commands | Default | Meaning |
| --- | --- | --- | --- |
| `--db PATH` | `serve`, `tui` | `up.db` | SQLite database; created if missing. |
| `--retention DURATION` | `serve` | `720h` | How long to keep results; `0` keeps everything. |

`up <command> -h` prints the full help.

## Contributing

Issues and pull requests are welcome. Run `go vet ./...` and `go test -race ./...` before
sending changes.
