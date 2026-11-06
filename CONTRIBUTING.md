# Contributing to up

Every change starts with a GitHub issue, including bugs, features, documentation, chores, CI and tooling. Open or find the issue **before implementation**. Creating the issue authorizes work: there is no separate approval step, approval label or required assignee.

## Quick path

1. Find or create an [issue](https://github.com/giulianotesta7/up/issues/new/choose) describing the motivation and acceptance criteria. Use the bug report or feature request form; documentation, chores, CI and tooling use a blank issue.
2. Create a branch from `main` named `type/slug`, for example `fix/check-timeout` or `ci/issue-first`. Supported prefixes are `feat`, `fix`, `docs`, `chore`, `ci`, `refactor` and `test`.
3. Implement the scoped change with tests and relevant documentation.
4. Run the applicable checks below and open a PR using the [PR template](.github/PULL_REQUEST_TEMPLATE.md).
5. Include a standalone closing line in the PR body, replacing the example number with your issue:

   ```text
   Closes #123
   ```

Keep commits focused and use Conventional Commit titles such as `fix: handle check timeout`.

## Issue-first check

The **PR issue** workflow validates the current PR body when a PR is opened, edited, reopened, synchronized or marked ready for review. It requires at least one `Closes #<number>` line referencing a real issue in this repository. Matching is case-insensitive; surrounding whitespace is allowed.

- PR numbers, missing issues, inline text, full URLs, cross-repository references and `Fixes #123` do not satisfy the check.
- References inside HTML comments do not count. Replace the template's placeholder with a real number.
- Labels and assignees are not approval gates. An existing closed issue also passes; use the issue that actually governs your work.
- The check validates linkage, not when implementation began. Opening the issue first remains a contributor responsibility.

For classification, the bug report and feature request forms apply the `bug` and `enhancement` labels. For a documentation-only blank issue, add the `documentation` label. CI, chores and tooling can remain unlabeled. Labels never delay starting work once the issue exists.

The workflow uses read-only permissions and runs validator code from the trusted PR base commit, never the PR head. Validator changes therefore take effect after landing on the base branch. The initial PR introducing these files cannot validate itself from a base that does not contain them.

Maintainers must configure **PR issue** as a required status check in the protection rule or ruleset for `main` to make it merge-blocking. Committing a workflow does not configure repository protection. On initial rollout, let the check register after the workflow lands, then require it.

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

For issue-validator changes, use Node.js 22 and run:

```sh
node --test .github/scripts/pr-issue.test.cjs
```

For release-workflow changes, run `python3 .github/tests/test_release_workflow.py`. Regenerate the demo only when needed with `sh docs/demo/record.sh` (requires vhs, ttyd and ffmpeg).

## Before review and merge

- Follow the PR template: Why, Summary, Verification, Not verified, and `Closes #<number>`.
- Report checks actually run and explicitly list anything not verified.
- Keep the change focused on its governing issue; update the issue if scope changes.
- Wait for all required checks, including documentation-only PRs. Local results are not proof that remote checks passed.
- Maintainers merge with a **merge commit**, not squash or rebase.
