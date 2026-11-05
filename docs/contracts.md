# Foundation contracts

What the five feature branches (advanced HTTP, TCP, webhooks, channel test,
maintenance) share. Schema version 6. Do not add another migration without
rebasing on this one; migrations are append-only.

## Types (`internal/config`)

| Type | Fields added |
|---|---|
| `Check` | `Kind` (`""`/`"http"` = HTTP, `"tcp"`), `Method` (`""` = GET), `Headers []Header`, `Body secret.Sealed`, `BodyEdit Edit`, `ExpectText`, `Host`, `Port` |
| `Header` | `Name`, `Value secret.Sealed`, `Edit Edit` |
| `Channel` | `WebhookOrigin`, `WebhookURL`/`WebhookURLEdit`, `WebhookAuthName`, `WebhookAuth`/`WebhookAuthEdit`; `Kind` is `KindEmail` or `KindWebhook` |
| `MaintenanceWindow` | `ID`, `Check`, `Weekdays []time.Weekday` (local start day), `StartMinute` 0..1439, `Duration`, `Zone` (IANA) |
| `ChannelTest` | `ID`, `Channel`, `Status` (`TestPending`/`TestSending`/`TestOK`/`TestError`), `Error`, `Requested`, `Started`, `Finished` |

- `Check` is not comparable with `==`. Use `a.SameDefinition(b)`; call `c.Canonical()` on external input (`"http"` and `"GET"` become `""`, empty headers become nil). A new field that changes how a check runs must be added to `SameDefinition` **and** to the `RecordDefined` SQL.
- `ValidateCheck`, `ValidateChannel`, `ValidateMaintenanceWindow` are the only validators. Bounds: methods `GET HEAD POST PUT PATCH DELETE OPTIONS`; 32 headers; header names are RFC tokens; sealed value limits `MaxSealedHeaderValue` (4096) and `MaxSealedBody` (64 KiB + 256); `MaxExpectText` 1024; TCP host without scheme/brackets/port (bare IPv6), port 1..65535, no HTTP-only fields, `expected_status` 0; window duration 1m..7d, weekdays distinct 0..6, zone loads and is not `Local` (tzdata is embedded).
- `ChannelKinds` (what the TUI offers) still lists only email; webhook is valid everywhere else. The webhook feature adds it to the form.

### Protected values

Header values, HTTP body, webhook URL and webhook auth value are `secret.Sealed` to the daemon public key (`secret.Seal`, same scheme as SMTP passwords). The daemon opens them with `*secret.Key.Open`; the store, snapshot and API never expose plain text or ciphertext, only `*_set` booleans (webhook: scheme+host in `webhook_origin`; build it with `config.WebhookOrigin(url)`).

`Edit` per value: `EditKeep` (zero), `EditReplace` (needs a non-empty sealed value), `EditClear`. Update: keep retains the stored value (headers match by name, case-insensitive, in order; a header absent from the list is removed). Create: keep stores the given value (empty if none). The webhook URL cannot be cleared.

### Maintenance evaluation

`config.EvaluateMaintenance(windows, at) Maintenance{Active, End}` is pure. Occurrence = `[start, start+Duration)`; a nonexistent local start (DST gap) is skipped that day, a repeated one runs once at its first occurrence; windows overlapping or touching union (`End` is the union's end, capped at `MaintenanceHorizon`, 366 days). Nothing calls it for suppression yet.

## Store (`internal/store`)

- Checks: `AddCheck`, `UpdateCheck`, `SetChecks`, `Checks` carry every field above; `RecordDefined` compares URL, kind, method, headers, body, expect text, host, port, interval, timeout, status.
- Channels: `AddChannel`, `UpdateChannel`, `Channels`, `Channel` carry the webhook fields (sealed values come back sealed). `DeleteChannel` also deletes its test requests.
- Windows (max `MaxMaintenanceWindows` = 20 per check): `AddMaintenanceWindow(ctx, w) (w with ID, error)`, `UpdateMaintenanceWindow(ctx, w)`, `DeleteMaintenanceWindow(ctx, check, id)`, `MaintenanceWindows(ctx, check)`, `AllMaintenanceWindows(ctx)`. `DeleteCheck` deletes its windows. Unknown check/window is `ErrNotFound`, invalid input `ErrValidation`.
- Channel tests (one active per channel, enforced by a unique index): `EnqueueChannelTest(ctx, channel, at)` (`ErrNotFound` unknown channel, `ErrExists` if pending/sending), `ClaimChannelTest(ctx, at)` (oldest pending to sending), `CompleteChannelTest(ctx, id, errText, at)` (empty text = ok; text is cleaned by `SafeTestError`; only a sending test completes), `FailSendingChannelTests(ctx, reason, at)` (call at daemon start), `LatestChannelTest(ctx, channel)`, `LatestChannelTests(ctx)`. History per channel is capped at 20.

## Delivery seam (`internal/alert`)

`(*Notifier).Deliver(ctx, check, result) error` and `DeliverCert(ctx, check, cert, remaining) error` look up `check.Channel`, send synchronously and return the error (also logged). The queue (`Run`) calls the same code. Transports: email (`email.go`, unchanged behavior) and webhook (`webhook.go`, returns `ErrWebhookUnavailable` until implemented). The channel-test entry point sits beside these: `(*Notifier).SendTest(ctx, channel) error` sends an event with `test` set (check and result zero, only `check.Channel` carries the name). `alert.TestRunner` is the daemon loop: `FailSendingChannelTests` at start, then about every 2s claim, send (30s limit), complete with a safe error, one at a time. Do not change the signatures above. The internal `event` has a `test bool` field: the channel-test entry point sets it (check and result zero, channel looked up by name); every transport must label a test event as a test (email subject/body, webhook `"event":"test"`). Transport signatures `deliverEmail`/`deliverWebhook(ctx, ch, e event) error` stay as they are.

## TUI (`internal/tui`)

`Snapshot.ChannelTests`, `Service.Maintenance`, `Service.InMaintenance` are filled by `Load` when the store implements the optional readers (`MaintenanceReader`, `ChannelTestReader`; `*store.Store` does). Optional writers, implemented by `*store.Store` and `*client.Client`: `tui.MaintenanceStore` (add/update/delete window) and `tui.ChannelTester` (`EnqueueChannelTest`). Type-assert them; `tui.Store` is unchanged.

## API (`internal/remote`)

All routes need the bearer token; bodies are strict JSON (unknown fields are 400); a rejected request changes nothing.

| Route | Body / response |
|---|---|
| `POST /api/v1/checks`, `PUT /api/v1/checks/{name}` | `CheckDTO`; 201 returns the read form, PUT 204 |
| `POST /api/v1/channels`, `PUT /api/v1/channels/{name}` | channel write body; 201 returns `ChannelDTO` |
| `GET /api/v1/checks/{name}/maintenance` | 200 `[MaintenanceDTO]` |
| `POST /api/v1/checks/{name}/maintenance` | `MaintenanceWriteDTO`; 201 `MaintenanceDTO` |
| `PUT`/`DELETE /api/v1/checks/{name}/maintenance/{id}` | `MaintenanceWriteDTO` / none; 204 |
| `POST /api/v1/channels/{name}/test` | none; 202 `ChannelTestDTO` (404 unknown channel, 409 one already active) |
| `GET /api/v1/channels/{name}/test` | 200 `ChannelTestDTO`, 404 if none |

JSON shapes:

```
check (write)   {"name","url","interval_ms","timeout_ms","expected_status","channel","alert_after","recover_after","tls_off",
                 "kind":"http|tcp","method","headers":[{"name","value":PROT}],"body":PROT,"expect_text","host","port"}
check (read)    same, but headers [{"name","value_set"}], "body_set", kind always set, method "GET" when empty
PROT            {"action":"keep|replace|clear","sealed":{"key":b64,"data":b64}}   sealed only with replace; absent = keep
channel (write) {"name","kind":"email|webhook","smtp_server","recipients","webhook_origin","webhook_url":PROT,"webhook_auth_name","webhook_auth":PROT}
channel (read)  {"name","kind","smtp_server","recipients","webhook_origin","webhook_url_set","webhook_auth_name","webhook_auth_set"}
window          {"id","weekdays":[0..6],"start_minute","duration_ms","zone"}   (write omits id)
channel test    {"id","channel","status":"pending|sending|ok|error","error","requested","started","finished"}
snapshot adds   services[].maintenance [window], services[].in_maintenance, services[].maintenance_end, channel_tests [channel test]
```

Sealed values must be sealed to the key in the snapshot's `public_key`; `sealed.key` must equal it (400 otherwise). `cmd/up/api.go` mounts the wildcard routes; `internal/client` implements the matching methods and `LoadSnapshot` shifts `in_maintenance`'s end and channel test times by clock skew.
