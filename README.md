# up

A small HTTP uptime monitor for a server, with a terminal UI.

`up serve` runs your HTTP checks permanently and stores definitions and every
result in SQLite. `up tui` creates monitors and shows current status, latency,
last check, failure reason, and the outages that happened while you were away.
Closing the TUI never stops the daemon's checks.

The TUI runs on the same server; to look at it from another machine, SSH in
and open it there.

```text
 up · 3 services · 2 up · 1 down · 0 pending

   SERVICE   STATUS   LATENCY  LAST CHECK
 > api       UP          84ms  3s ago
   website   UP         142ms  8s ago
   payments  DOWN        12ms  2s ago      unexpected status 503 (expected 200)

 ↑/↓ select · enter details · n new monitor · q quit
```

## Install

```sh
go install github.com/giulianotesta7/up/cmd/up@latest
```

Requires Go 1.26.10 or newer. The binary has no runtime dependencies (SQLite is
compiled in, no cgo).

## Quick start

No YAML is required. In a directory writable by your user, start the daemon:

```sh
up serve --db up.db   # keep this running
```

In another terminal in the same directory (or over SSH), open the same database:

```sh
up tui --db up.db
```

Press `n` to create a monitor. Enter a unique name and an HTTP/HTTPS URL; the
form starts with interval `30s`, timeout `5s`, and expected status `200`.
Use `tab`/`shift+tab` to move between fields, `enter` to save, or `esc` to cancel
before submitting. Cancellation is disabled while a save is in progress.
Invalid or duplicate submissions show an error without changing definitions or
history; cancelling before submission also leaves them unchanged.

Both commands create the database on first use, so you can also open the TUI
first. Keep `up serve` running against that same database to perform checks.
It discovers saved monitors about every 2 seconds without a restart or
restarting existing checks. Definitions and history persist across restarts;
creation is supported, editing and deletion are not.

## Commands and TUI

| Command    | Flags (defaults)                                  | What it does                                                      |
| ---------- | ------------------------------------------------- | ----------------------------------------------------------------- |
| `up serve` | `--db up.db`, optional `--config PATH` (no default) | Runs stored checks right away, then at their intervals, until SIGINT or SIGTERM. An empty database is OK. |
| `up tui`   | `--db up.db`                                      | Creates monitors and displays stored results, refreshing every 2s. |

`up --help` lists commands; `up <command> -h` prints command help. Relative DB
paths resolve from the current directory; use the same absolute path when
running from different directories or under systemd.

### TUI keys

| Key                 | Action outside the form              |
| ------------------- | ------------------------------------ |
| `↑`/`↓` or `k`/`j`  | Select a service                     |
| `enter`             | Show its details                     |
| `n`                 | Open the new-monitor form            |
| `esc` or `backspace` | Back to the list                     |
| `q` or `ctrl+c`      | Quit (the daemon keeps checking)     |

In the form, `tab`/`shift+tab` changes focus, `enter` saves, `esc` cancels
before submission, and `ctrl+c` quits. Other text keys, including `q`, edit
the focused field.

The details show the check settings, last error, how long it has been down,
the recent checks (oldest → newest) and the recent outages, for example
`Oct 25 03:12:00 → Oct 25 03:40:00 (28m0s)`.

`up serve` writes a heartbeat every 10s. If the TUI sees none for 30s, it shows
``daemon not responding: … — is `up serve` running?`` and keeps every service
at its last known status, because stale data says nothing about the services.
New monitors remain PENDING until a result is recorded.

## Monitor settings and optional YAML import

| Field             | Meaning                                                           |
| ----------------- | ----------------------------------------------------------------- |
| `name`            | Unique name shown in the TUI.                                     |
| `url`             | `http` or `https` URL requested with `GET`.                       |
| `interval`        | Positive Go duration (`30s`, `1m`, `1h30m`).                     |
| `timeout`         | Positive duration; must not exceed `interval`.                   |
| `expected_status` | HTTP status (100-599) that counts as up.                          |

A check is **up** when the final response (redirects are followed) has
`expected_status` within `timeout`. Otherwise it is **down**, with the reason:
the unexpected status, `timeout after 5s`, or the connection error. Checks measure
availability from the server where `up serve` runs.

SQLite is the source of monitor definitions. For an optional batch import,
use the format in [`up.example.yaml`](up.example.yaml):

```yaml
checks:
  - name: api
    url: https://api.example.com/health
    interval: 30s
    timeout: 5s
    expected_status: 200
```

Every YAML field is required (the form's defaults do not apply to imports),
names must be unique, and unknown fields are rejected. Import explicitly when
starting the daemon, instead of the ordinary `serve` command:

```sh
up serve --config /absolute/path/monitors.yaml --db /absolute/path/up.db
```

The import appends new definitions; identical existing definitions are no-ops.
A conflicting same-name definition rejects the entire import atomically.
Invalid imports report an error and start no checks. Rejected imports leave
existing definitions and history unchanged. Successful imports never delete
existing monitors or history, and the YAML file is never modified.
Without `--config PATH`, no YAML is read; editing a YAML file does not change
stored monitors.

## Run as a systemd service

[`deploy/up.service`](deploy/up.service) runs `up serve` as a dedicated `up`
user with `/var/lib/up/up.db`. No configuration file is needed. On a systemd
host, install the binary and unit (create the user only if it does not exist):

```sh
sudo install -m 0755 "$(go env GOPATH)/bin/up" /usr/local/bin/up
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin up
sudo install -m 0644 deploy/up.service /etc/systemd/system/up.service
sudo systemctl daemon-reload
sudo systemctl enable --now up
journalctl -u up -f   # logs: start, up/down changes, errors
```

Systemd creates `/var/lib/up` owned by the service user. The unit keeps its
filesystem protections while allowing writes there. Over SSH, open the TUI as
the same user, with the same absolute database path:

```sh
sudo -u up /usr/local/bin/up tui --db /var/lib/up/up.db
```

This allows monitor creation as well as reading results, including SQLite's
WAL files. Do not run the TUI as root or make the database world-writable.
Press `n` to add monitors; the running service discovers them without restarting.

## Limitations

- HTTP checks only: no TCP, cron heartbeats, or CPU/RAM/disk monitoring.
- Monitor creation only; no editing or deletion.
- No alerts yet: down services are visible in the TUI and in the logs.
- No remote API/client; access is through SSH.
- Results are kept forever; the database grows with every check.

## Development

```sh
go test -race ./...
```

CI runs `gofmt`, `go mod tidy`, `go vet`, the race tests, `staticcheck` and
`govulncheck` (see [`.github/workflows`](.github/workflows)).
