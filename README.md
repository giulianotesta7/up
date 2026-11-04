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
- **Email alerts.** One email when a monitor goes down and one when it recovers, sent over SMTP and configured from the TUI.
- **TLS expiry warnings.** HTTPS monitors show the days left on their certificate and email you at 14, 7 and 3 days before it expires.
- **Temporary mute.** Hold a monitor's emails for 30 minutes to 7 days, for example during a deploy. Checks and history keep running and the mute ends by itself.
- **Quieter alerts.** Optionally wait for several failures in a row before the first email, so one timeout does not wake anyone up.
- **Remote access (optional).** Run `up tui --remote` on your laptop to manage a daemon running elsewhere, over an authenticated API, with no SSH or `docker exec`. Off unless you turn it on; see [Remote access](#remote-access).

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
| `--remote` | `tui` | off (`UP_REMOTE=true` turns it on) |

The database is resolved in this order: `--db`, `$UP_DB`, `/var/lib/up/up.db` if it exists, `~/.config/up/up.db`. The TUI footer shows which one is open.

New monitors default to a 30s interval, 5s timeout and expected status 200.

### Keys

`j`/`k` move · `enter` details · `n` new · `e` edit · `d` delete · `m` mute · `u` unmute · `s` SMTP servers · `c` channels · `a` toggle animation · `esc` back · `q` quit

With `--remote`, `r` also reloads right away (see [Remote access](#remote-access)).

In forms: `tab`/`shift+tab` switch fields · `←`/`→` choose an option · `enter` save · `esc` cancel.

### Alerts

Alerts go from a monitor to a **channel**, and from the channel to an **SMTP server**:

1. Press `s` and add an SMTP server: host, port, TLS mode, username, password and sender address. For a relay that does not require authentication, set **No login required** (above **Username**) to **Yes** with `←`/`→`; username and password are skipped, and no encryption key is needed. When editing, this option reflects the saved server; switching to **Yes** removes its stored credentials on save. TLS remains a separate setting.
2. Press `c` and add a channel: pick the SMTP server and list one or more recipients, separated by commas.
3. Edit a monitor (`e`) and choose the channel. Monitors without a channel never alert.

`up serve` sends one email when a monitor goes down and one when it recovers, never one per failed check. Restarting the daemon does not resend.

#### Fewer false alarms

Two monitor fields control when those emails go out. Both default to 1, which alerts on the first change.

- **Alert after**: failed checks in a row before the down email (1, 2, 3, 5 or 10).
- **Recover after**: successful checks in a row before the recovery email.

Every check is still stored; only the email waits. While the streak is short of the threshold the list shows `FAILING 2/3` (or `RECOVERING 1/2`), and the form shows how long a real outage takes to reach you (for example `3 × 30s = 1m30s`). A daemon restart keeps the streak in progress. Databases created by an earlier version are upgraded by `up serve`.

TLS modes: `starttls` (default, usually port 587), `tls` (usually 465) and `none` for local relays. `none` cannot be combined with a username, so a password never travels in plain text.

SMTP passwords are never stored in plain text. The first `up serve` run creates a private key next to the database (`up.db.key`, mode `0600`, readable only by the daemon) and stores the matching public key in the database. The TUI encrypts passwords with the public key, and only the daemon can decrypt them. Run `up serve` once before entering a password.

- **Back up `up.db.key` together with `up.db`.** Without it, stored passwords cannot be decrypted: the daemon creates a new key and the TUI marks those passwords as `needs re-entry`.
- `up serve` refuses to start if the key file is readable or writable by group or others. Fix it with `chmod 600`.

#### Muting during maintenance

Press `m` on a monitor and pick 30 minutes, 1 hour, 4 hours, 24 hours or a custom duration (1m to 7d); `u` ends the mute early. While muted the list shows the time left, and checks and history continue.

When the mute ends, recipients get only what they do not already know:

- Still down, and they were not told yet: one down email, giving the real outage start.
- Back up, and the outage began during the mute: nothing.
- Back up, but they had been told it was down before the mute: the recovery email.

The daemon notices an end within one check interval. Muting is stored in the database, so it survives restarts.

#### Certificate expiry

For `https://` monitors `up` reads the certificate from the connection it already makes: no extra requests, and validation stays on, so an invalid or expired certificate still fails the check. The list gets a `CERT` column with the days left (amber under 14, red under 3) and the detail view shows the host, issuer, expiry and which warnings went out.

The monitor's channel gets one email per threshold (14, 7 and 3 days), remembered across restarts. A certificate first seen with 2 days left produces one email, not three. A renewed certificate resets the thresholds without sending anything, and warnings wait while the monitor is muted. After a failed check the detail view shows the last known certificate and when it was observed.

Set `TLS expiry` to `off` in the monitor form to skip all of this for one monitor (for example an internal service with a self-managed certificate): no warnings, no `CERT` value, and the stored certificate is forgotten. It is `on` by default and has no effect on `http://` monitors.

## Remote access

`up serve` can offer an authenticated API so a TUI on another machine manages it, with no SSH. Everything here is opt-in: without `--api-listen` no port is opened and nothing changes.

### On the server

```sh
up serve --api-listen :8443 --tls-cert cert.pem --tls-key key.pem
```

Pick how the API is served. Without one of these it only accepts loopback addresses (`127.0.0.1`):

| Mode | Flags | Use it when |
|---|---|---|
| HTTPS | `--tls-cert` and `--tls-key` | `up` terminates TLS itself |
| Behind a proxy | `--behind-proxy` | a reverse proxy in front terminates TLS |
| Plain HTTP | `--no-tls` | the network is already encrypted (a VPN, Tailscale). The token is **not** encrypted on the wire and `up serve` prints a warning. |

`--tls-cert` and `--tls-key` go together. `--no-tls` needs `--api-listen` and cannot be combined with the other three. Setting any of them without `--api-listen` is refused at startup instead of being ignored.

Every API flag also reads an environment variable, handy for containers; a flag on the command line wins:

| Flag | Variable |
|---|---|
| `--api-listen ADDR` | `UP_API_LISTEN` |
| `--tls-cert FILE` / `--tls-key FILE` | `UP_TLS_CERT` / `UP_TLS_KEY` |
| `--behind-proxy` | `UP_BEHIND_PROXY` (`true` or `false`) |
| `--no-tls` | `UP_NO_TLS` (`true` or `false`) |

There is one access token. The first start prints it once on stderr (`up: access token (shown once): up_...`) and keeps only its hash in `<db>.token` (mode `0600`). `up token reset [--db PATH]` creates a new one, prints it once on stdout and stops the old one working immediately, even while `up serve` is running. A token file that other users can read is refused.

### On your machine

```sh
up login                  # server URL and a masked token; saved only if the server accepts it
up tui --remote           # manage the remote server (or UP_REMOTE=true up tui)
up logout                 # forget the saved remote
```

- `up login` opens a modal. `--url URL` pre-fills the address. For scripts, `--token-stdin` (it needs `--url`) reads the token from a pipe instead, never from an argument, so it stays out of your shell history and the process list: `echo "$TOKEN" | up login --url https://up.example.com --token-stdin`. A plain `http://` URL is allowed and shows a warning. A self-signed certificate is rejected with a hint to use one from a trusted CA or to add it to your system trust store.
- The remote is saved in `~/.config/up/remote.json` (`$XDG_CONFIG_HOME/up/remote.json` if set), mode `0600`; `up` refuses to read it if others can. `up logout` only deletes that file: it does not contact the server or revoke the token (use `up token reset` for that).
- `up tui --remote` is always explicit: a saved remote is never used on its own. It cannot be combined with `--db`, and it never opens or creates a local database.
- The footer shows `● remote · <url>`. If the server stops answering it shows `● disconnected`, keeps the last data on screen and retries every couple of seconds; `r` retries immediately.

### What the API exposes

All routes need `Authorization: Bearer <token>`; the token is never accepted in a URL. Without it every route answers the same `401`.

| Request | Does |
|---|---|
| `GET /api/v1/ping` | checks the token and that the server is `up` |
| `GET /api/v1/snapshot` | everything the TUI shows, in one request |
| `POST /api/v1/checks`, `PUT`/`DELETE /api/v1/checks/{name}` | add, edit, delete a monitor (a name cannot change) |
| `PUT`/`DELETE /api/v1/checks/{name}/mute` | mute (`duration_ms`, 1 minute to 7 days) or unmute |
| `POST /api/v1/smtp-servers`, `PUT`/`DELETE /api/v1/smtp-servers/{name}` | SMTP servers |
| `POST /api/v1/channels`, `PUT`/`DELETE /api/v1/channels/{name}` | channels |

Errors use the same texts as the local TUI: `400` invalid, `404` not found, `409` duplicate or still in use. Request bodies are strict JSON of at most 1 MiB, and a rejected request changes nothing.

### Security notes

- **SMTP passwords never travel in plain text.** The client seals them with the server's public key and sends only `password_sealed`; the API refuses a plaintext `password`. Responses carry just `none`, `set` or `needs_reentry`, never the password or its ciphertext, and the private key stays on the server.
- The token is stored hashed (SHA-256) on the server and compared in constant time.
- The client does not follow redirects, so the token is never sent to another host. Text sent by the server is stripped of terminal escape sequences before it is drawn.
- There are no roles and no second token: whoever has the token can manage everything. There is no rate limiting and no certificate pinning, so if the API is reachable from the internet put it behind a reverse proxy that limits attempts.

### Docker (optional)

Docker is not required: `up serve` runs fine under systemd. The repository has a `Dockerfile` and an example `docker-compose.yml`:

```sh
docker compose up -d
docker compose logs up        # the first start prints the access token once
```

- Configure it with the environment variables above; the flags work too.
- Everything that must survive lives in the `/data` volume: the database, its encryption key (`up.db.key`) and the token hash (`up.db.token`). Back the three up together; without the key, stored SMTP passwords have to be entered again.
- The container runs as an unprivileged user (uid 10001). The example publishes the port on `127.0.0.1` only: put a TLS reverse proxy in front (`UP_BEHIND_PROXY`), or publish it on an already encrypted network and set `UP_NO_TLS`.
- To replace the token: `docker compose exec up up token reset`.

## Contributing

Run `go vet ./...` and `go test -race ./...` before opening a PR. Regenerate the demo with `sh docs/demo/record.sh` (needs vhs, ttyd and ffmpeg).

## License

MIT
