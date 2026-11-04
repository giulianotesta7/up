<div align="center">

<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/banner-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="docs/images/banner-light.svg">
    <img alt="up" src="docs/images/banner-light.svg" width="640">
  </picture>
</h1>

Before contributing, read [CONTRIBUTING.md](CONTRIBUTING.md) for the issue-first workflow.

Self-hosted HTTP uptime monitor with a terminal UI. One binary and a SQLite file: no database server, no agents, no YAML.

[![Go quality](https://github.com/giulianotesta7/up/actions/workflows/quality.yml/badge.svg)](https://github.com/giulianotesta7/up/actions/workflows/quality.yml)
[![Go security](https://github.com/giulianotesta7/up/actions/workflows/security.yml/badge.svg)](https://github.com/giulianotesta7/up/actions/workflows/security.yml)

</div>

<img src="docs/images/demo.gif" alt="up TUI: monitors, latency trends and a detail chart" width="760">

## Features

- **HTTP checks** with per-monitor interval, timeout and expected status. Redirects are followed.
- **Managed from the TUI.** Add, edit, delete and mute monitors; the daemon picks up changes within ~2s.
- **Remote TUI.** Run the daemon on a server and manage it from your laptop over an authenticated API.
- **Check history.** Every result is stored and outages are derived from it. Results are kept for 30 days by default; retention is configurable.
- **Email alerts** on down and recovery, with optional thresholds so a single timeout does not wake anyone up.
- **TLS expiry warnings** at 14, 7 and 3 days.
- **Maintenance mute** from 1 minute to 7 days, without stopping checks or history.
- **Stale-daemon detection.** If the daemon stops, the TUI says so and keeps the last known statuses instead of marking everything DOWN.

## Install

### Docker

1. Start the daemon:

   ```sh
   git clone https://github.com/giulianotesta7/up.git && cd up
   docker compose up -d
   docker compose logs up | grep 'access token'
   ```

2. [Install the binary](#binary) on your machine and connect:

   ```sh
   up login --url "http://<server ip>:8080"
   up tui --remote    # press n to add your first monitor
   ```

The `up-data` volume holds `up.db`, the SMTP encryption key (`up.db.key`) and the API token hash (`up.db.token`). Back them up together.

> Checks run inside the container, where `localhost` is the container itself. To monitor the host, use `host.docker.internal` with `extra_hosts: ["host.docker.internal:host-gateway"]`, or `network_mode: host`.

### Binary

Linux (amd64/arm64):

```sh
curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh
```

Run `up serve` and `up tui` in separate terminals, or install a service below.

### systemd

1. Install and start a service:

   **User service** — linger enabled; database at `~/.config/up/up.db`:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh -s -- --service
   ```

   **System service** — runs as the `up` user; shared database at `/var/lib/up/up.db`:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh -s -- --system
   ```

2. With `--system`, run `newgrp up` or log in again for group access.

3. Open the TUI (`n` adds a monitor):

   ```sh
   up tui
   ```

Existing databases are not moved automatically. To manage the service from another machine, enable [Remote access](#remote-access).

### Version and uninstall

Pin a version or uninstall (databases, user and group are kept):

```sh
curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh -s -- --version vX.Y.Z
curl -fsSL https://raw.githubusercontent.com/giulianotesta7/up/main/install.sh | sh -s -- --uninstall
```

### From source

Requires Go 1.26.10:

```sh
go build -o up ./cmd/up
```

## Usage

```sh
up serve        # run checks and store results
up tui          # view and manage monitors on this machine
up tui --remote # manage a remote server (after up login)
```

The database is resolved in this order: `--db`, `$UP_DB`, `/var/lib/up/up.db` if it exists, `~/.config/up/up.db`. The TUI footer shows which one is open. New monitors default to a 30s interval, 5s timeout and expected status 200.

### Keys

`j`/`k` move · `enter` details · `n` new · `e` edit · `d` delete · `m` mute · `u` unmute · `s` SMTP servers · `c` channels · `a` animation · `r` retry now (remote) · `esc` back · `q` quit.

In forms: `tab`/`shift+tab` switch fields · `←`/`→` choose · `enter` save · `esc` cancel.

## Alerts

1. `s`: add an SMTP server. For a relay without authentication, set **No login required** to **Yes**.
2. `c`: add a channel with that server and one or more comma-separated recipients.
3. `e` on a monitor: pick the channel. Monitors without a channel never alert.

`up serve` sends one email when a monitor goes down and one when it recovers, never one per failed check. **Alert after** and **Recover after** (default 1) set how many consecutive failures or successes are needed; meanwhile the list shows `FAILING 2/3` or `RECOVERING 1/2`. Restarting the daemon preserves the streak and does not resend alerts.

TLS modes: `starttls` (default, usually port 587), `tls` (usually 465) and `none` for relays without login. `none` cannot be combined with a username.

Run `up serve` once before entering SMTP passwords to create `up.db.key` (required mode `0600`). **Back up the key with the database**; losing it requires re-entering passwords.

### Muting

`m` offers presets and custom durations from 1 minute to 7 days; `u` ends the mute early. Checks and history keep running, and mutes survive restarts.

When the mute ends, recipients only hear what they do not already know: a down email if it is still down and they were not told, or a recovery email if they were told about the outage before the mute. An outage that starts and ends during the mute sends nothing.

### Certificate expiry

HTTPS monitors get a `CERT` column with the days left (amber under 14, red under 3) and one email per threshold: 14, 7 and 3 days. A certificate first seen with 2 days left sends one warning, not three. Renewal resets the warnings silently; muting delays them.

Set `TLS expiry` to `off` in a monitor's form to disable certificate tracking and warnings. **TLS validation stays on**: an invalid or expired certificate still fails the check.

## Remote access

Enable the HTTP API:

```sh
up serve --api-listen :8080
```

HTTPS is recommended: HTTP sends the token and data unencrypted. Use a TLS-terminating reverse proxy or a certificate and key:

```sh
up serve --api-listen :8443 --tls-cert cert.pem --tls-key key.pem
```

The certificate and key must be supplied together and require `--api-listen`.

The access token is printed once on the first API start (stderr / service logs). Its hash is stored in `<db>.token` (mode `0600`). Rotate it with `up token reset [--db PATH]`, or in Docker:

```sh
docker compose exec up up token reset
```

### Connect

```sh
up login          # server URL and masked token; saved only if accepted
up tui --remote   # manage the remote server
up logout         # forget it (does not revoke the token)
```

For scripts:

```sh
echo "$TOKEN" | up login --url https://up.example.com --token-stdin
```

Credentials are saved in `$XDG_CONFIG_HOME/up/remote.json` (default `~/.config/up/remote.json`), mode `0600`. `--remote` cannot be combined with `--db`. Disconnections retry automatically; `r` retries immediately. Self-signed certificates need system trust.

### API reference

Every route requires `Authorization: Bearer <token>`; tokens in URLs are not accepted. Unauthenticated requests return `401`.

| Request | Does |
|---|---|
| `GET /api/v1/ping` | validates the token and server identity |
| `GET /api/v1/snapshot` | returns everything the TUI shows |
| `POST /api/v1/checks`, `PUT`/`DELETE /api/v1/checks/{name}` | adds, edits or deletes a monitor (names cannot change) |
| `PUT`/`DELETE /api/v1/checks/{name}/mute` | mutes (`duration_ms`, 1 minute to 7 days) or unmutes |
| `POST /api/v1/smtp-servers`, `PUT`/`DELETE /api/v1/smtp-servers/{name}` | manages SMTP servers |
| `POST /api/v1/channels`, `PUT`/`DELETE /api/v1/channels/{name}` | manages channels |

Bodies are strict JSON, limited to 1 MiB. Errors: `400` invalid input, `404` not found, `409` duplicate or still in use. Rejected requests change nothing.

### Security

- **One token, full access.** There are no roles or rate limiting. For an internet-facing API, a proxy with rate limiting is recommended.
- SMTP passwords are sealed with the server's public key; the API rejects plaintext passwords and never returns passwords or ciphertext. The private key stays on the server.
- The server stores a SHA-256 token hash and compares it in constant time. Token and remote credential files readable by others are refused.
- The client does not follow redirects or forward the token to another host. Server text is stripped of terminal escape sequences before display. There is no certificate pinning.

## Configuration

A command-line flag overrides its environment variable.

| Flag | Variable | Command | Default |
|---|---|---|---|
| `--db PATH` | `UP_DB` | `serve`, `tui`, `token reset` | [database resolution](#usage) |
| `--retention DURATION` | `UP_RETENTION` | `serve` | `720h` (`0` keeps everything) |
| `--api-listen ADDR` | `UP_API_LISTEN` | `serve` | off |
| `--tls-cert FILE` | `UP_TLS_CERT` | `serve` | |
| `--tls-key FILE` | `UP_TLS_KEY` | `serve` | |
| `--remote` | `UP_REMOTE` | `tui` | `false` |
| `--url URL` | | `login` | |
| `--token-stdin` | | `login` | `false` |

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) for the required issue-first workflow, development checks and PR conventions.

## License

MIT
