# Foundation contracts

What the five feature branches (advanced HTTP, TCP, webhooks, channel test,
maintenance) share, plus the defaults added after them. Schema version 7 (v7
adds the single-row `defaults` table). Do not add another migration without
rebasing on this one; migrations are append-only.

## Types (`internal/config`)

| Type | Fields added |
|---|---|
| `Check` | `Kind` (`""`/`"http"` = HTTP, `"tcp"`), `Method` (`""` = GET), `Headers []Header`, `Body secret.Sealed`, `BodyEdit Edit`, `ExpectText`, `Conditions []Condition`, `Host`, `Port` |
| `Condition` | `Kind` (`ConditionJSON` `"json"`, `ConditionRegex` `"regex"`, `ConditionResponseTime` `"response_time"`, `ConditionHeader` `"header"`), `Path`, `Name`, `Value`, `Pattern`, `Within`; `Summary()`, `ConditionLabel(kind)`, `ConditionKinds`, `SensitiveHeader(name)`, `JSONPath(path)` |
| `Header` | `Name`, `Value secret.Sealed`, `Edit Edit` |
| `Defaults` | `Zone`, `Channel` (`""` = none), `Interval`, `Timeout`, `ExpectedStatus`, `AlertAfter`, `RecoverAfter`; `BuiltinDefaults()` is UTC, no channel, 30s, 5s, 200, 1, 1; `ValidateDefaults` reuses `ValidateZone` and `ValidateCheck` and requires thresholds 1..10 |
| `Channel` | `WebhookOrigin`, `WebhookURL`/`WebhookURLEdit`, `WebhookAuthName`, `WebhookAuth`/`WebhookAuthEdit`; `Kind` is `KindEmail` or `KindWebhook` |
| `MaintenanceWindow` | `ID`, `Check`, `Weekdays []time.Weekday` (local start day), `StartMinute` 0..1439, `Duration`, `Zone` (IANA) |
| `ChannelTest` | `ID`, `Channel`, `Status` (`TestPending`/`TestSending`/`TestOK`/`TestError`), `Error`, `Requested`, `Started`, `Finished` |

- `Check` is not comparable with `==`. Use `a.SameDefinition(b)`; call `c.Canonical()` on external input (`"http"` and `"GET"` become `""`, empty headers become nil). A new field that changes how a check runs must be added to `SameDefinition` **and** to the `RecordDefined` SQL.
- `ValidateCheck`, `ValidateChannel`, `ValidateMaintenanceWindow` are the only validators. Bounds: methods `GET HEAD POST PUT PATCH DELETE OPTIONS`; 32 headers; header names are RFC tokens and unique case-insensitively; `HEAD` with `ExpectText` is rejected; sealed value limits `MaxSealedHeaderValue` (4096) and `MaxSealedBody` (64 KiB + 256); `MaxExpectText` 1024; at most `MaxConditions` (32) conditions, paths/values/patterns at most `MaxConditionText` (1024) bytes, JSON paths non-empty dot-separated segments, regex compiles, header names are tokens, `Within` > 0, whole milliseconds and < `Timeout`, each condition sets only its kind's fields (others are rejected, e.g. `condition 1: pattern is not used by JSON value conditions`), JSON/regex conditions not with `HEAD`, messages prefixed `condition N: `; TCP host without scheme/brackets/port (bare IPv6), port 1..65535, no HTTP-only fields, `expected_status` 0; window duration 1m..7d, weekdays distinct 0..6, zone loads and is not `Local` (tzdata is embedded).
- `ChannelKinds` (what the TUI offers) lists email and webhook.

### Heartbeats (schema v9)

`Check.Kind` `"heartbeat"` (`config.CheckHeartbeat`) adds `Period` (1m..31d) and `Grace` (0..31d); `URL`, `Method`, `ExpectedStatus`, `Interval`, `Timeout`, headers, body, expected text, host, port and `TLSOff` must be zero, and `Period`/`Grace` must be zero for HTTP and TCP. Both are in `SameDefinition` and `RecordDefined`. The ping token is not a `Check` field. `config.APIListener{Address, TLS}` is the daemon's API (`Enabled`, `BaseURL(hostname)`; an unspecified host becomes `hostname`); `config.HeartbeatPath(token)` and `HeartbeatFailSuffix` build ping paths.

Results (`internal/check/heartbeat.go`): `Ping` (up), `Fail` (down, `reported failure`, `Failed(r)`), missed deadlines (down, `no ping since 15:04 MST` or `Jan 2 15:04 MST`, `Missed(r)`: the last contact in `loc`, nil is UTC, labeled with the zone abbreviation, e.g. `-03`, `CET`, `UTC`; the date appears when the contact and the deadline fall on different days in `loc`). `NextDeadline(c, newest) = newest + Period + Grace` after any result; `Overdue(c, newest, lastContact, now, loc)` returns the misses due (now strictly after the deadline), at most the first and the latest. The monitor passes the Defaults zone, read when a miss is due (UTC when it cannot be read or loaded), so a change applies to the next miss and the stored reason reads the same in the TUI, remote and alerts. The monitor runs no `Runner` for heartbeats: every discovery interval it observes new stored results and records misses with `RecordMissed`, through the same streak, mute, maintenance and alert path as HTTP/TCP.

### Protected values

Header values, HTTP body, webhook URL and webhook auth value are `secret.Sealed` to the daemon public key (`secret.Seal`, same scheme as SMTP passwords). The daemon opens them with `*secret.Key.Open`; the store, snapshot and API never expose plain text or ciphertext, only `*_set` booleans (webhook: scheme+host in `webhook_origin`; build it with `config.WebhookOrigin(url)`).

`Edit` per value: `EditKeep` (zero), `EditReplace` (needs a non-empty sealed value), `EditClear`. Update: keep retains the stored value (headers match by name, case-insensitive, in order; a header absent from the list is removed). Create: keep stores the given value (empty if none). The webhook URL cannot be cleared.

### Maintenance evaluation

`config.EvaluateMaintenance(windows, at) Maintenance{Active, End}` is pure. Occurrence = `[start, start+Duration)`; a nonexistent local start (DST gap) is skipped that day, a repeated one runs once at its first occurrence; windows overlapping or touching union (`End` is the union's end, capped at `MaintenanceHorizon`, 366 days). The monitor (`internal/monitor/maintenance.go`) is its only caller for suppression: alerts and certificate warnings are held while a manual mute or an active window applies, and the existing mute reconciliation sends what recipients do not know once, when both have ended (`monitor.Options.Now` injects the clock; `monitor.WindowStore` is the optional store API).

## Store (`internal/store`)

- Checks: `AddCheck`, `UpdateCheck`, `SetChecks`, `Checks` carry every field above; `RecordDefined` compares URL, kind, method, headers, body, expect text, conditions, host, port, interval, timeout, status. Conditions (v10 `checks.conditions`, a deterministic JSON list of `{k,p,n,v,r,w}`, `''` = none) hold no secrets.
- Channels: `AddChannel`, `UpdateChannel`, `Channels`, `Channel` carry the webhook fields (sealed values come back sealed). `DeleteChannel` also deletes its test requests.
- Windows (max `MaxMaintenanceWindows` = 20 per check): `AddMaintenanceWindow(ctx, w) (w with ID, error)`, `UpdateMaintenanceWindow(ctx, w)`, `DeleteMaintenanceWindow(ctx, check, id)`, `MaintenanceWindows(ctx, check)`, `AllMaintenanceWindows(ctx)`. `DeleteCheck` deletes its windows. Unknown check/window is `ErrNotFound`, invalid input `ErrValidation`.
- Defaults (v7 `defaults`, one row with `id = 1`; no row reads as `BuiltinDefaults`): `Defaults(ctx)`, `SetDefaults(ctx, d)` (invalid input or a channel that does not exist is `ErrValidation`; nothing is stored). `DeleteChannel` sets a default channel of that name to none, so reads never name a deleted channel.
- Alert state (v6 `alert_state`, a trigger deletes it with its check): `AlertState(ctx, check) (known, found, err)`, `SetAlertState(ctx, check, known)` (`ErrNotFound` unknown check), `AlertStates(ctx)`. It records whether recipients were last told the check is down; the monitor writes it when it sends or settles and reads it on loop start (a live mute's `was_alerting` first, no row falls back to the streak). Snapshot: `Service.KnownAlerting`, `services[].known_alerting`.
- Heartbeats (v9 adds `checks.period_ns`, `grace_ns`, `ping_token`, `ping_token_key`, `ping_token_hash` with a unique partial index, and the one-row `daemon_api` table): adding or making a heartbeat creates a token (`uphb_` + 256 bits base64url) sealed to the stored public key (`ErrValidation` without one) plus its SHA-256; edits keep it, another kind clears it, delete removes it, copies get their own. `HeartbeatToken(ctx, name) (secret.Sealed, error)`, `RegenerateHeartbeatToken(ctx, name) (string, error)` (`ErrNotFound` unless a heartbeat), `PingHeartbeat(ctx, token, failed, at) (found, error)` (hash lookup, constant-time compare), `RecordMissed(ctx, c, newest, r) (stored, error)` (only while the definition is unchanged and no result is newer than `newest`), `SetAPIListener`/`APIListener` (written by every `up serve` start; zero = no API).
- Channel tests (one active per channel, enforced by a unique index): `EnqueueChannelTest(ctx, channel, at)` (`ErrNotFound` unknown channel, `ErrExists` if pending/sending), `ClaimChannelTest(ctx, at)` (oldest pending to sending), `CompleteChannelTest(ctx, id, errText, at)` (empty text = ok; text is cleaned by `SafeTestError`; only a sending test completes), `FailSendingChannelTests(ctx, reason, at)` (call at daemon start), `LatestChannelTest(ctx, channel)`, `LatestChannelTests(ctx)`. History per channel is capped at 20.

## Delivery seam (`internal/alert`)

`(*Notifier).Deliver(ctx, check, result) error` and `DeliverCert(ctx, check, cert, remaining) error` look up `check.Channel`, send synchronously and return the error (also logged). The queue (`Run`) calls the same code. Transports: email (`email.go`, unchanged behavior) and webhook (`webhook.go`: JSON POST, 2xx = delivered, 10 s timeout, no redirects, no retries, sanitized errors; honors `e.test`). The channel-test entry point sits beside these: `(*Notifier).SendTest(ctx, channel) error` sends an event with `test` set (check and result zero, only `check.Channel` carries the name). `alert.TestRunner` is the daemon loop: `FailSendingChannelTests` at start, then about every 2s claim, send (30s limit), complete with a safe error, one at a time. Do not change the signatures above. The internal `event` has a `test bool` field: the channel-test entry point sets it (check and result zero, channel looked up by name); every transport must label a test event as a test (email subject/body, webhook `"event":"test"`). Transport signatures `deliverEmail`/`deliverWebhook(ctx, ch, e event) error` stay as they are.

## TUI (`internal/tui`)

`Snapshot.ChannelTests`, `Service.Maintenance`, `Service.InMaintenance` are filled by `Load` when the store implements the optional readers (`MaintenanceReader`, `ChannelTestReader`; `*store.Store` does). Optional writers, implemented by `*store.Store` and `*client.Client`: `tui.MaintenanceStore` (add/update/delete window), `tui.ChannelTester` (`EnqueueChannelTest`) and `tui.DefaultsStore` (`SetDefaults`); `Load` fills `Snapshot.Defaults` through the optional `tui.DefaultsReader` (zero value means built-in, see `Snapshot.NewItemDefaults`). Type-assert them; `tui.Store` is unchanged.

Heartbeats: `Service.Heartbeat` (`HeartbeatState{Waiting, LastPing, Deadline}`), `Service.Label()` `WAITING` before the first result, `Snapshot.API` (`config.APIListener`, via the optional `APIListenerReader`). `tui.HeartbeatTokens` (`RevealHeartbeatToken`, `RegenerateHeartbeatToken`) is implemented by `*client.Client` and, locally, by `NewLocalHeartbeatTokens(store, dbPath)`, which reads `<db>.key` on each reveal and never creates it; when it cannot open the token the error wraps `tui.ErrRevealUnavailable` and its text is the hint to show.

## API (`internal/remote`)

All routes need the bearer token except heartbeat pings; bodies are strict JSON (unknown fields are 400); a rejected request changes nothing.

| Route | Body / response |
|---|---|
| `POST /api/v1/checks`, `PUT /api/v1/checks/{name}` | `CheckDTO`; 201 returns the read form, PUT 204 |
| `POST /api/v1/channels`, `PUT /api/v1/channels/{name}` | channel write body; 201 returns `ChannelDTO` |
| `GET /api/v1/checks/{name}/maintenance` | 200 `[MaintenanceDTO]` |
| `POST /api/v1/checks/{name}/maintenance` | `MaintenanceWriteDTO`; 201 `MaintenanceDTO` |
| `PUT`/`DELETE /api/v1/checks/{name}/maintenance/{id}` | `MaintenanceWriteDTO` / none; 204 |
| `POST /api/v1/channels/{name}/test` | none; 202 `ChannelTestDTO` (404 unknown channel, 409 one already active) |
| `GET /api/v1/channels/{name}/test` | 200 `ChannelTestDTO`, 404 if none |
| `GET /api/v1/defaults` | 200 `DefaultsDTO` |
| `PUT /api/v1/defaults` | `DefaultsDTO` (every field); 200 `DefaultsDTO`, 400 on invalid input or an unknown channel |
| `GET /api/v1/deliveries?monitor=&channel=&limit=` | 200 `{"server_time","attempts":[{"at","monitor","channel","event","outcome","reason"}]}` newest first; `limit` defaults to 50, max 500; 400 on an invalid limit |
| `GET`/`POST /api/v1/checks/{name}/heartbeat-token` | none; 200 `{"token","path"}` (GET opens the stored token with the daemon key, POST replaces it); 404 not a heartbeat, 409 sealed to another key |
| `POST /api/v1/heartbeat/{token}`, `POST /api/v1/heartbeat/{token}/fail` | **no bearer token** (`remote.HeartbeatRoutes`, mounted before authentication on `remote.HeartbeatPrefix`); body ignored, at most 1 KiB (413); 200 `{"ok":true}`; any other token or path below the prefix 404 `{"error":"not found"}`, byte-identical; 405 `Allow: POST`; 429 `{"error":"too many requests"}` `Retry-After: 1` over `DefaultPingLimits` (safety cap on every request 500/s burst 1000; per presented token 1/s burst 5; tokens that match no heartbeat also per connection address 20/s burst 100 and globally 50/s burst 200; forwarded headers are not trusted); 500 `{"error":"internal error"}` |

JSON shapes:

```
check (write)   {"name","url","interval_ms","timeout_ms","expected_status","channel","alert_after","recover_after","tls_off",
                 "kind":"http|tcp","method","headers":[{"name","value":PROT}],"body":PROT,"expect_text","conditions":[COND],"host","port"}
check (read)    same, but headers [{"name","value_set"}], "body_set", kind always set, method "GET" when empty
COND            {"kind":"json|regex|response_time|header","path","name","value","pattern","within_ms"}   only the kind's fields (another kind's field is 400 `condition N: ... not used by ...`); same in read
PROT            {"action":"keep|replace|clear","sealed":{"key":b64,"data":b64}}   sealed only with replace; absent = keep
channel (write) {"name","kind":"email|webhook","smtp_server","recipients","webhook_origin","webhook_url":PROT,"webhook_auth_name","webhook_auth":PROT}
channel (read)  {"name","kind","smtp_server","recipients","webhook_origin","webhook_url_set","webhook_auth_name","webhook_auth_set"}
window          {"id","weekdays":[0..6],"start_minute","duration_ms","zone"}   (write omits id)
defaults        {"zone","channel","interval_ms","timeout_ms","expected_status","alert_after","recover_after"}
channel test    {"id","channel","status":"pending|sending|ok|error","error","requested","started","finished"}
snapshot adds   services[].maintenance [window], services[].in_maintenance, services[].maintenance_end, channel_tests [channel test], defaults
heartbeat       check adds "period_ms","grace_ms" (kind "heartbeat"; write omits them for other kinds); services[].heartbeat {"waiting","last_ping","deadline"} (heartbeats only); snapshot "api" {"listen","tls"}
```

Sealed values must be sealed to the key in the snapshot's `public_key`; `sealed.key` must equal it (400 otherwise). `cmd/up/api.go` mounts the wildcard routes; `internal/client` implements the matching methods and `LoadSnapshot` shifts `in_maintenance`'s end and channel test times by clock skew.
