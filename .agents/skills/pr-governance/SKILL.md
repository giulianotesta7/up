---
name: pr-governance
description: "Trigger: creating, preparing, or reviewing a pull request in this repository. Apply up's issue linkage, branch naming, PR template, actual CI gates and merge-commit policy."
license: MIT
metadata:
  author: "giulianotesta7"
  version: "1.0"
---

## Activation Contract

Use when an up change is about to be pushed for a pull request, needs a PR created or edited, or is being checked for review readiness.

Do not use for read-only Q&A or local work that will not become a PR. Loading this skill does not authorize pushing, opening a PR or merging.

## Precedence

Generic `branch-pr` and `gentle-ai-branch-pr` skills do not apply here. up has no shellcheck or PR-validation workflows, and does not use their `type:*` labels. `.github/workflows/` contains `quality.yml`, `security.yml` and `release.yml`. Follow this project skill rather than importing checks from another repository.

## Hard Rules

- Every PR links its governing issue with `Closes #<n>`. The issue is required by `issue-governance`; creating it authorizes the work, without an approval label or approval step.
- The PR body follows `.github/PULL_REQUEST_TEMPLATE.md`: Why, Summary, Verification, Not verified, and the issue-closing line. Describe observed verification, not intended checks.
- PR labels are optional. Use only the existing GitHub defaults permitted by `issue-governance`; never invent `type:*` labels.
- Every required check must pass before merge. Check definitions live under `.github/workflows/`: Go quality, Go security / staticcheck and Go security / govulncheck. Point at those definitions rather than duplicating their flags. Pending or failing required checks block merge, including on documentation-only PRs.
- Preserve the merge-commit style used in this repository. With explicit merge authorization, use `gh pr merge --merge`, not squash or rebase. Never interpret readiness as permission to merge.

## Branch Naming

Format: `type/slug`, lowercase and without spaces.

| Prefix | Use for |
| --- | --- |
| `feat/` | User-facing capability |
| `fix/` | Incorrect behavior |
| `docs/` | Documentation only |
| `chore/` | Tooling, skills and repository configuration |
| `ci/` | CI and release workflow changes |
| `refactor/` | Restructuring without intended behavior change |
| `test/` | Test-only changes |

Branch and merge history show `feat/`, `docs/`, `chore/`, `ci/` and `refactor/`. `fix/` and `test/` are permitted for their named change types, not claimed as observed history. Do not impose an issue-number slug requirement on existing branches; issue linkage belongs in the PR body.

## What Proves A Pull Request Here

- `.github/workflows/quality.yml`: formatting, dependency tidiness, vet, build and race tests
- `.github/workflows/security.yml`: staticcheck and govulncheck
- `.github/workflows/release.yml`: release publication, not a shellcheck or PR-validation gate
- `.agents/skills/go-tests/SKILL.md`: test layers and local evidence
- `.agents/skills/issue-governance/SKILL.md`: issue requirement and permitted labels

Do not report another repository's checks as missing here. Do not call a local command run proof that remote required checks passed.

## Commit Format

Use conventional titles consistent with history, such as `feat: ...`, `docs: ...` or `ci(release): ...`. Scopes are optional. PRs retain branch commits through a merge commit; the PR title is not a squash-commit message and does not replace the individual commit history.

## Decision Gates

| Condition | Result |
| --- | --- |
| Governing issue is absent or not linked with `Closes #<n>` | BLOCKED for PR readiness |
| A required check fails or is pending | BLOCKED for merge |
| Issue is linked and required checks pass | GO for readiness, not authorization to publish or merge |
| A configured release job does not run on the PR | Not a missing PR gate |

## Output Contract

Return:

- `pr`: number/URL or "none";
- `issue_linked`: true/false, with the exact closing line;
- `branch_valid`: true/false, with the naming deviation if any;
- `gate`: GO or BLOCKED, naming missing issue linkage or required checks;
- verification performed and anything not verified.

## References

Paths below are repository-relative.

- `AGENTS.md`: skill precedence and workflow gates
- `.agents/skills/issue-governance/SKILL.md`: issue requirement and labels
- `.github/PULL_REQUEST_TEMPLATE.md`: body contract
- `.github/workflows/`: actual check and release definitions
