---
name: git-workflow
description: "Trigger: starting any change to up, creating or triaging an issue, labeling, planning work, creating a branch, or creating, preparing, reviewing or merging a pull request. Issues are optional and never a gate; permitted labels are applied best-effort; pull requests follow the template, the required checks and the merge-commit method."
license: MIT
metadata:
  author: "heohlee"
  version: "1.0"
---

## Activation Contract

Use before the first write of any up change (feature, bug, chore, docs or skill edit), when an issue needs creating, labeling or triaging, when planning transitions to implementation, and when a change is pushed for a pull request, needs a PR created or edited, or is checked for review readiness.

Do not use for read-only work: Q&A, reading, searching, inspecting history, running tests or building. Loading this skill does not authorize GitHub writes, pushing, opening a PR or merging.

Generic PR, branch or issue instructions installed in your agent describe other repositories and do not apply here. up has no shellcheck workflow and does not use `type:*` labels. Follow this skill rather than importing checks from another repository, and do not reconcile the two.

## Hard Rules

- An issue is optional for every change and every contributor: a pull request may arrive without one, and no CI check requires it. Recommend opening an issue first only for large or design-changing work, so the idea can be discussed before code is written. Never block, close or delay a change for lacking an issue, and never require an approval label or an approval round trip. Do not reintroduce issue or approval requirements through labels, title conventions or contributor roles.
- One change, one branch, based on an updated `main`.
- Every required check must pass before merge, including on documentation-only PRs. Never interpret readiness as permission to merge.
- Never put tokens, credentials or personal data in issues, PR bodies, comments, screenshots or logs. Summarize or redact logs.
- Respect the current task's authorization: if GitHub writes are prohibited, report the proposed issue, labels or PR without creating or editing anything remotely.

## Issues

An issue is optional; when one exists, it governs the change: it is the intent anchor and the target of the PR's `Closes #<n>` line. Search for an existing issue and reuse it if present. For large or design-changing work without one, propose one (motivation, acceptance criteria) and create it when authorized before implementation. Otherwise begin implementation right away; no approval step is needed. Keep its classification truthful as scope changes, subject to authorization for remote edits.

### Issue Forms

`.github/ISSUE_TEMPLATE/` has a bug report form and a feature request form. Documentation, chores, CI and tooling issues use a blank issue.

### Label Taxonomy

| Group | Rule | Values |
| --- | --- | --- |
| Type | Exactly one for bugs, capabilities or docs-only work | `bug`: incorrect behavior; `enhancement`: new capability; `documentation`: documentation only |
| Maintenance | No label | Chores, CI and tooling issues remain unlabeled; do not invent a chore type |
| Triage | Only when applicable, not a type or approval gate | GitHub defaults: `duplicate`, `good first issue`, `help wanted`, `invalid`, `question`, `wontfix` |

Use only these GitHub default labels, on issues and PRs alike; never create labels, invent `type:*` labels or import another repository's taxonomy. There are no area or status labels in this taxonomy; do not add them. Apply the best fit best-effort and note uncertainty; missing or imperfect labels never block or delay work. Classification belongs to the issue; PR labels are optional and need not duplicate it. An issue that is only a question or triage record needs no type label.

The permitted set is `bug`, `documentation`, `duplicate`, `enhancement`, `good first issue`, `help wanted`, `invalid`, `question`, `wontfix`. Verify the live inventory with `gh label list --repo heohlee/up`; do not claim it contains only these labels without checking. At adaptation time, that read also returned `accessibility`, outside this permitted taxonomy. Its presence does not authorize using it or changing the remote inventory.

Labels are informational for now. `.github/release.yml` does not exist; `.github/workflows/release.yml` is a different file. Only claim release-note grouping by PR labels if an actual `.github/release.yml` configures it. Issue labels alone would not prove that grouping.

## Branches

Branch `type/slug`, lowercase and without spaces, created from an updated `main`, for example `fix/check-timeout` or `docs/remote-access`.

| Prefix | Use for |
| --- | --- |
| `feat/` | User-facing capability |
| `fix/` | Incorrect behavior |
| `docs/` | Documentation only |
| `chore/` | Tooling, skills and repository configuration |
| `ci/` | CI and release workflow changes |
| `refactor/` | Restructuring without intended behavior change |
| `test/` | Test-only changes |

Branch and merge history show `feat/`, `docs/`, `chore/`, `ci/` and `refactor/`. `fix/` and `test/` are permitted for their named change types, not claimed as observed history. Do not invent other prefixes or impose an issue-number slug requirement on existing branches; issue linkage belongs in the PR body.

## Pull Requests

- The PR body follows `.github/PULL_REQUEST_TEMPLATE.md`: Why, Summary, Verification, Not verified, and the optional issue-closing line. Describe observed verification, not intended checks; anything not tried belongs in Not verified.
- When an issue exists, link it with a standalone `Closes #<n>` line. No check requires the line; a missing issue or link never blocks a PR.
- Pending or failing required checks block merge. Check definitions live under `.github/workflows/`; point at them rather than duplicating their flags, and do not claim a check that has no workflow.
- Preserve the merge-commit style used in this repository. With explicit merge authorization, use `gh pr merge --merge`, not squash or rebase.

### Required checks

- Go quality (`.github/workflows/quality.yml`)
- Go security / staticcheck (`.github/workflows/security.yml`)
- Go security / govulncheck (`.github/workflows/security.yml`)

No check validates issue linkage.

### What Proves A Pull Request Here

- `.github/PULL_REQUEST_TEMPLATE.md`: body contract
- `CONTRIBUTING.md`: contributor-facing workflow, local checks and rollout requirements
- `.github/workflows/quality.yml`: formatting, dependency tidiness, vet, build and race tests
- `.github/workflows/security.yml`: staticcheck and govulncheck
- `.github/workflows/release.yml`: release publication, not a shellcheck or PR-validation gate

Release workflows run on pushed tags, not on PRs; they are not missing PR gates. Do not report another repository's checks as missing here. Do not call a local command run proof that remote required checks passed.

### Commit Format

Use Conventional Commit titles consistent with history, such as `feat: ...`, `fix: ...`, `docs: ...` or `ci(release): ...`. Scopes are optional. Keep commits focused. PRs retain branch commits through a merge commit; the PR title is not a squash-commit message and does not replace the individual commit history.

## Decision Gates

| Condition | Result |
| --- | --- |
| No issue exists for the change | GO: the issue is optional; suggest one only for large or design-changing work |
| Issue exists | GO: no approval wait |
| Bug, capability or docs issue has missing or conflicting type labels | GO: apply or propose the best-fit single type; never block over labels |
| Label is ambiguous or outside the permitted set | GO: note or propose a correction; do not create labels |
| Chore, CI or tooling issue is unlabeled | GO: this is the intended classification |
| No issue, or an issue the PR does not link | GO: suggest adding `Closes #<n>` when an issue exists; never block on it |
| Branch name or prefix deviates | GO: report the deviation in `branch_valid` |
| A required check fails or is pending | BLOCKED for merge |
| Required checks pass | GO for readiness, not authorization to publish or merge |
| A configured release job does not run on the PR | Not a missing PR gate |

## Output Contract

Return:

- `issue`: number/URL or "none"; `labels_valid`: true/false, with the exact taxonomy deviation if false;
- `branch`: name and base commit; `branch_valid`: true/false, with the naming deviation if any;
- `pr`: number/URL or "none"; `issue_linked`: true/false, with the exact closing line if any (informational);
- `gate`: GO or BLOCKED, naming only failing or pending required checks; an absent issue or imperfect labels never block;
- verification performed, anything not verified, and proposed corrections or GitHub writes not performed because authorization was absent.

## References

Paths below are repository-relative.

- `CONTRIBUTING.md`: contributor-facing workflow and rollout requirements
- `.github/ISSUE_TEMPLATE/`: bug report and feature request forms
- `.github/PULL_REQUEST_TEMPLATE.md`: body contract
- `.github/workflows/`: actual check and release definitions
- GitHub label inventory: `gh label list --repo heohlee/up`
