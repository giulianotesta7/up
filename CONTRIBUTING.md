# Contributing to up

Pull requests are welcome without an issue: opening one first is optional. For large or design-changing work, an issue lets us agree on the idea before you write code. There is no approval step, approval label or required assignee.

## Quick path

1. Optionally, find or create an [issue](https://github.com/heohlee/up/issues/new/choose) describing the motivation and acceptance criteria; this is recommended for large or design-changing work. Use the bug report or feature request form; documentation, chores, CI and tooling use a blank issue.
2. Create a branch from `main` named `type/slug`, for example `fix/check-timeout` or `ci/release-notes`. Supported prefixes are `feat`, `fix`, `docs`, `chore`, `ci`, `refactor` and `test`.
3. Implement the scoped change with tests and relevant documentation.
4. Run the applicable checks below and open a PR using the [PR template](.github/PULL_REQUEST_TEMPLATE.md).
5. If the PR resolves an issue, include a standalone closing line in the PR body, replacing the example number with your issue:

   ```text
   Closes #123
   ```

Keep commits focused and use Conventional Commit titles such as `fix: handle check timeout`.

## Development and checks

Install the Go version declared in [go.mod](go.mod). From the repository root, run the quality checks defined in [quality.yml](.github/workflows/quality.yml):

```sh
gofmt -l .                     # must produce no output
go mod tidy
git diff --exit-code -- go.mod go.sum
go vet ./...
go build ./...
go test ./... -race -count=1 -timeout 10m
```

The [security workflow](.github/workflows/security.yml) also runs:

```sh
go install honnef.co/go/tools/cmd/staticcheck@v0.7.0
staticcheck ./...
go install golang.org/x/vuln/cmd/govulncheck@v1.7.0
govulncheck ./...
```

For release-workflow changes, run `python3 .github/tests/test_release_workflow.py`. Regenerate the demo only when needed with `sh docs/demo/record.sh` (requires vhs, ttyd and ffmpeg).

## Before review and merge

- Follow the PR template: Why, Summary, Verification, Not verified, and `Closes #<number>` if the PR resolves an issue.
- Report checks actually run and explicitly list anything not verified.
- Keep the change focused; if it has a governing issue, update the issue when scope changes.
- Wait for all required checks, including documentation-only PRs. Local results are not proof that remote checks passed.
- Pull requests are merged with a **merge commit**, not squash or rebase.
