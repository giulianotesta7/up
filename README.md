# up

A small HTTP uptime monitor for a server, with a terminal UI.

`up serve` runs your HTTP checks permanently and stores every result in SQLite.
`up tui` shows those results: current status, latency, last check, failure
reason, and the outages that happened while you were away. Closing the TUI
never stops the checks.

The TUI runs on the same server; to look at it from another machine, SSH in
and open it there.

```text
 up · 3 services · 2 up · 1 down · 0 pending

   SERVICE   STATUS   LATENCY  LAST CHECK  
 > api       UP          84ms  3s ago
   website   UP         142ms  8s ago
   payments  DOWN        12ms  2s ago      unexpected status 503 (expected 200)

 ↑/↓ select · enter details · q quit
```

## Install

```sh
go install github.com/giulianotesta7/up/cmd/up@latest
```

Requires Go 1.26.10 or newer. The binary has no runtime dependencies (SQLite is
compiled in, no cgo).

## Configure

Checks live in a YAML file (see [`up.example.yaml`](up.example.yaml)):

```yaml
checks:
  - name: api
    url: https://api.example.com/health
    interval: 30s
    timeout: 5s
    expected_status: 200
```

| Field             | Meaning                                                           |
| ----------------- | ----------------------------------------------------------------- |
| `name`            | Unique name shown in the TUI.                                     |
| `url`             | `http` or `https` URL requested with `GET`.                       |
| `interval`        | Time between checks, as a Go duration (`30s`, `1m`, `1h30m`).     |
| `timeout`         | Maximum time to wait for the response; must not exceed `interval`. |
| `expected_status` | HTTP status (100-599) that counts as up.                          |

Every field is required and unknown fields are rejected. `up serve` reports
every problem in the file and does not start until it is valid.

A check is **up** when the final response (redirects are followed) has
`expected_status` within `timeout`. Otherwise it is **down**, with the reason:
the unexpected status, `timeout after 5s`, or the connection error.

The checks run from the server where `up serve` runs, so they measure
availability from that server.

## Run

```sh
up serve --config up.yaml --db up.db   # keep this running
up tui --db up.db                      # in another terminal, or over SSH
```

| Command    | Flags (defaults)                       | What it does                                                                  |
| ---------- | -------------------------------------- | ----------------------------------------------------------------------------- |
| `up serve` | `--config up.yaml`, `--db up.db`       | Runs every check right away and then at its interval until SIGINT or SIGTERM. |
| `up tui`   | `--db up.db`                           | Reads the database every 2s. It never writes and never creates the database.  |

`up <command> -h` prints the help of a command.

### TUI keys

| Key                  | Action                               |
| -------------------- | ------------------------------------ |
| `↑`/`↓` or `k`/`j`   | Select a service                     |
| `enter`              | Show its details                     |
| `esc` or `backspace` | Back to the list                     |
| `q` or `ctrl+c`      | Quit (the checks keep running)       |

The details show the check settings, last error, how long it has been down,
the recent checks (oldest → newest) and the recent outages, for example
`Oct 25 03:12:00 → Oct 25 03:40:00 (28m0s)`.

`up serve` writes a heartbeat every 10s. If the TUI sees none for 30s, it shows
``daemon not responding: … — is `up serve` running?`` and keeps every service
at its last known status, because stale data says nothing about the services.

## Run as a systemd service

[`deploy/up.service`](deploy/up.service) runs `up serve` as a dedicated `up`
user, reading `/etc/up/up.yaml` and writing `/var/lib/up/up.db`:

```sh
sudo install -m 0755 "$(go env GOPATH)/bin/up" /usr/local/bin/up
sudo useradd --system --no-create-home --shell /usr/sbin/nologin up
sudo install -d -m 0755 /etc/up
sudo install -m 0644 up.example.yaml /etc/up/up.yaml   # then edit it
sudo install -m 0644 deploy/up.service /etc/systemd/system/up.service
sudo systemctl daemon-reload
sudo systemctl enable --now up
journalctl -u up -f                                    # logs: start, up/down changes, errors
```

Open the TUI as the same user, so it can read the database:

```sh
sudo -u up up tui --db /var/lib/up/up.db
```

After editing `/etc/up/up.yaml`, apply it with `sudo systemctl restart up`.

## Limitations

- HTTP checks only: no TCP, cron heartbeats, or CPU/RAM/disk monitoring.
- No alerts yet: down services are visible in the TUI and in the logs.
- No remote access other than SSH.
- Results are kept forever; the database grows with every check.

## Development

```sh
go test -race ./...
```

CI runs `gofmt`, `go mod tidy`, `go vet`, the race tests, `staticcheck` and
`govulncheck` (see [`.github/workflows`](.github/workflows)).
