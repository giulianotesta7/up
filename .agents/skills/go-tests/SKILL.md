---
name: go-tests
description: "Trigger: Go tests, check execution, SQLite persistence, monitor scheduling, TUI behavior, CLI validation, installer tests, or Go CI evidence. Govern up's test layers and their real gates."
license: MIT
metadata:
  author: "giulianotesta7"
  version: "1.0"
---

## Activation Contract

Use when changing Go behavior or `*_test.go` files, the installer behavior covered by `deploy/install_test.go`, the SQLite schema, or Go gates in `.github/workflows/quality.yml` and `security.yml`.

Do not use for passive documentation or release copy with no test-behavior impact.

## Layer Map

Identify the package before choosing a harness. These are up's layers, not a separate domain/application architecture.

| Layer | Package | Real vs faked |
| --- | --- | --- |
| HTTP checks | `internal/check` | Real HTTP client against `httptest.NewServer`; fixed injected `Checker.Now`. A connection-refusal case briefly binds and closes a local port |
| Persistence | `internal/store` | Real file-backed `modernc.org/sqlite`, initialized through `Open` with the production schema, WAL and single writer connection |
| Monitor | `internal/monitor` | Real temporary store; fake check runner. Wrappers around the real store exercise operational failures and pruning |
| Terminal UI | `internal/tui` | Real SQLite for snapshot loading and creation; model messages and key events for views. Pure model cases may use value snapshots; inject the clock through `New` |
| Validation | `internal/config` | Pure check values and table-driven validation; no database or HTTP |
| Commands | `cmd/up` | In-process `runCmd` with captured exit code/stdout/stderr, real temporary databases and HTTP test servers; replace `runTUIProgram` to avoid a real terminal |
| Installer | `deploy` | `install_test.go` executes `../install.sh` with local fake release archives, temporary destinations and PATH stubs for system operations |

## Hard Rules

1. **Keep persistence real where it is already real.** Reuse `openTemp`, `openStore`, `seededSnapshot` and the package's existing helpers. Do not replace store/monitor/TUI persistence tests with an in-memory fake. Monitor failure wrappers may override an operation while retaining the underlying real store.
2. **Use the production SQLite driver and schema.** Arrange data through `store.Open`, `AddCheck`, `SetChecks`, `Record` and `Beat`, as applicable. Never substitute hand-created tables for the real initialization path. Raw SQL remains appropriate for schema/version rejection tests. Keep read-only, reopen, ordering, retention and rejected-write assertions truthful.
3. **Isolate paths before resolving defaults.** In `cmd/up`, reuse `isolateDefaultDatabase`: it sets `XDG_CONFIG_HOME`, `HOME` and `UP_DB`, redirects the package variable `systemDatabasePath` to a temporary path and restores it in cleanup. The system path is a Go variable, not an environment variable. Tests must never touch the real `~/.config/up` or `/var/lib/up`. Do not parallelize tests that change environment, working directory or package globals.
4. **Installer system operations are PATH stubs only.** Never run real `systemctl`, `sudo`, `useradd`, `usermod` or `loginctl`. Reuse the local release and install helpers; redirect install, unit and state paths into temporary directories. Stub identity and ownership commands when the scenario needs them. Verify command logs and preserved data rather than modifying the host's users or services.
5. **Inject time where the code accepts it.** Use fixed `Checker.Now` and TUI clocks for deterministic results and stale-state boundaries. Monitor scheduling currently uses real time, so do not claim every service has a clock seam.
6. **Bound asynchronous waits.** Deadline polling is established practice: monitor's `waitFor` has a five-second deadline and a short polling sleep; CLI and TUI tests also use deadline loops. Prefer channels or these bounded helpers over bare fixed sleeps to wait for an asynchronous effect. Absence assertions legitimately use a bounded sleep, such as proving no writes after shutdown or no duplicate scheduling across discovery polls. Do not describe this suite as sleep-free.
7. **Do not add listening services to tests.** Use `httptest` servers for HTTP check behavior, including CLI integration that runs real checks. The existing connection-refusal test binds and immediately closes a local socket to obtain an unused address; it is not a persistent service. Close servers and stores and cancel/join goroutines in cleanup.
8. **Use disposable storage with sufficient space.** Prefer `t.TempDir()` and cleanup. `/tmp` may be a quota-limited tmpfs: when needed, set `GOTMPDIR` for Go build work and `TMPDIR` for test scratch to suitable temporary storage outside the worktree. Report environmental limits rather than weakening tests.
9. **Closing evidence is the exact quality workflow gate set.** `.github/workflows/quality.yml` is authoritative for the formatting shell block, `go mod tidy` plus the dependency diff check, vet, build and race tests. Its race command is `go test ./... -race -count=1 -timeout 10m`. Do not substitute a non-race run. `.github/workflows/security.yml` defines the staticcheck and govulncheck gates and pinned tool versions. There is no coverage workflow or threshold here.

## Decision Gates

| Condition | Action |
| --- | --- |
| Validation or a pure model case proves the contract | Prefer that layer over a heavier integration test |
| Behavior depends on persistence, discovery, retention or snapshot loading | Exercise the real temporary SQLite store |
| A schema change is proposed | Extend initialization, version compatibility and reopen tests in `internal/store`; there is no numbered migration-test series |
| A test resolves database defaults or installer destinations | Isolate paths before execution |
| An asynchronous effect must appear | Use channels or deadline polling; never an unbounded loop |
| A required race or CI gate cannot run | Report the exact failure and missing evidence; do not claim closing verification |

## Execution Steps

1. Read the existing tests in the owning package and reuse its helpers and fixture paths.
2. Add a focused behavior test, observe its intended failure, then implement the smallest change. Preserve previous behavior through the public interface.
3. Run the focused package first with `-race`, then the exact closing gates from `quality.yml` when changing tested code. Run the applicable security gates from `security.yml`.
4. Record exact commands, results, environmental limits and skipped gates. Documentation-only work needs proportionate structural checks, not invented Go test evidence.

## Output Contract

Report the layer/package, files changed, focused command and result, closing gate commands and results, and anything skipped or blocked. Never present a build or a non-race test as race evidence.

## References

Paths below are repository-relative.

- `internal/check/check_test.go`: HTTP servers, clock and refusal probe
- `internal/store/store.go`, `internal/store/store_test.go`: schema, real store and lifecycle tests
- `internal/monitor/monitor_test.go`: real store, fake runner and deadline helper
- `internal/tui/tui_test.go`, `internal/tui/form_test.go`: snapshots, model and creation tests
- `internal/config/validation_test.go`: validation boundaries
- `cmd/up/db_test.go`, `cmd/up/main_test.go`: path isolation and command harness
- `deploy/install_test.go`: local releases and PATH stubs
- `.github/workflows/quality.yml`, `.github/workflows/security.yml`: closing gates
