<div align="center">

<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/banner-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="docs/images/banner-light.svg">
    <img alt="up" src="docs/images/banner-light.svg" width="640">
  </picture>
</h1>

Self-hosted HTTP uptime monitor with a terminal UI. One binary and a SQLite file: no database server, no agents, no YAML.

[![Go quality](https://github.com/giulianotesta7/up/actions/workflows/quality.yml/badge.svg)](https://github.com/giulianotesta7/up/actions/workflows/quality.yml)
[![Go security](https://github.com/giulianotesta7/up/actions/workflows/security.yml/badge.svg)](https://github.com/giulianotesta7/up/actions/workflows/security.yml)

</div>

<img src="docs/images/demo.gif" alt="up TUI: monitors, latency trends and a detail chart" width="760">

## Features

- **HTTP checks** with per-monitor interval, timeout and expected status. Redirects are followed.
- **Manage monitors from the TUI.** The running daemon picks up changes within ~2s, no restart needed.
- **Full history.** Every check is stored and outages are derived from it, so you can SSH back in hours later and see what you missed.
- **Stale-daemon detection.** If the daemon stops, the TUI says so and keeps the last known statuses instead of marking everything DOWN.
- **Retention.** Results older than 30 days are pruned. Monitor definitions are kept.

## Install

Linux (amd64/arm64):

```sh
curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh
```

To also run it as a systemd service, append `-s -- <flag>`:

| Flag | Runs as | Database |
|---|---|---|
| `--service` | your user (user unit, linger enabled) | `~/.config/up/up.db` |
| `--system` | system user `up`, shared through the `up` group | `/var/lib/up/up.db` |

Other flags: `--version vX.Y.Z`, `--uninstall` (keeps databases, user and group).

With `--system`, the installer adds you to the `up` group; run `newgrp up` or log in again before using `up tui`. Existing databases are never migrated automatically.

From source (Go 1.26.10):

```sh
go build -o up ./cmd/up
```

## Usage

```sh
up serve   # run checks and store results
up tui     # view and manage monitors (press n to add one)
```

| Flag | Commands | Default |
|---|---|---|
| `--db PATH` | `serve`, `tui` | see below |
| `--retention DURATION` | `serve` | `720h` (`0` keeps everything) |

The database is resolved in this order: `--db`, `$UP_DB`, `/var/lib/up/up.db` if it exists, `~/.config/up/up.db`. The TUI footer shows which one is open.

New monitors default to a 30s interval, 5s timeout and expected status 200.

### Keys

`j`/`k` move · `enter` details · `n` new · `e` edit · `d` delete · `a` toggle animation · `esc` back · `q` quit

In forms: `tab`/`shift+tab` switch fields · `enter` save · `esc` cancel.

## Contributing

Run `go vet ./...` and `go test -race ./...` before opening a PR. Regenerate the demo with `sh docs/demo/record.sh` (needs vhs, ttyd and ffmpeg).

## License

MIT
