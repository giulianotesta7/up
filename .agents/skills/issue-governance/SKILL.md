---
name: issue-governance
description: "Trigger: creating an issue, starting a feature, fixing a bug, labeling, planning work, or any change to up. Require a GitHub issue before implementation, self-authorized on creation, with existing default labels applied best-effort."
license: MIT
metadata:
  author: "giulianotesta7"
  version: "1.0"
---

## Activation Contract

Use when an up change is about to start, an issue needs creating or triaging, planning transitions to implementation, or existing work needs its governing issue checked.

Do not use for read-only Q&A with no repository change.

## Hard Rules

- This repository is issue-first: every change, including features, bugs, documentation, chores, CI and tooling, needs a GitHub issue BEFORE implementation. No issue, no work. This is the only blocking issue-governance gate; other project checks still apply.
- Creating the issue authorizes it. There is a single maintainer and no approval step. Never require an approval label or invent a separate approval round trip.
- Use only the GitHub default labels listed below. Never create labels or import another repository's taxonomy. Apply the best fit best-effort and note uncertainty; missing or imperfect labels never block or delay work.
- Classification belongs to the governing issue. PR labels are optional and need not duplicate it.
- Respect the current task's authorization: if GitHub writes are prohibited, report the missing issue or proposed labels without creating or editing anything remotely.

### Label Taxonomy

| Group | Rule | Values |
| --- | --- | --- |
| Type | Exactly one for bugs, capabilities or docs-only work | `bug`: incorrect behavior; `enhancement`: new capability; `documentation`: documentation only |
| Maintenance | No label | Chores, CI and tooling issues remain unlabeled; do not invent a chore type |
| Triage | Only when applicable, not a type or approval gate | `duplicate`, `good first issue`, `help wanted`, `invalid`, `question`, `wontfix` |

There are no area or status labels in this taxonomy. Do not add them. An issue that is only a question or triage record does not need a change-type label.

The permitted set is `bug`, `documentation`, `duplicate`, `enhancement`, `good first issue`, `help wanted`, `invalid`, `question`, `wontfix`. Verify live inventory with `gh label list`; do not claim it contains only these labels without checking. At adaptation time, that read also returned `accessibility`, outside this permitted taxonomy. Its presence does not authorize using it or changing the remote inventory.

Labels are informational for now. `.github/release.yml` does not exist; `.github/workflows/release.yml` is a different file. Only claim release-note grouping by PR labels if an actual `.github/release.yml` configures it. Issue labels alone would not prove that grouping.

### Why there is no approval gate

With a single maintainer, requester and approver are the same person. The issue remains the intent anchor and the target of the PR's `Closes #<n>` line, but creating it is sufficient authorization to begin. Do not reintroduce approval requirements through labels or title conventions.

## Decision Gates

| Condition | Result |
| --- | --- |
| No issue exists for the requested change | BLOCKED: create the issue first when authorized |
| Issue exists | GO: respect other project skills, with no approval wait |
| Bug, capability or docs issue has missing or conflicting type labels | GO: apply or propose the best-fit single type; never block over labels |
| Chore, CI or tooling issue is unlabeled | GO: this is the intended classification |
| Label is ambiguous or outside the permitted defaults | GO: note or propose a correction; do not create labels |

## Execution Steps

1. Search for the governing issue. Reuse it if present; otherwise, when authorized, create one with motivation and acceptance criteria before implementation.
2. Apply exactly one type for bugs, capabilities or docs-only work. Leave chores, CI and tooling unlabeled. Use triage labels only where meaningful and note uncertainty without waiting.
3. Begin implementation once the issue exists. No approval step is needed.
4. Keep classification truthful as scope changes, subject to authorization for remote edits.
5. Link the delivered PR back with `Closes #<n>`, following `pr-governance`.

## Output Contract

Return:

- `issue`: number/URL of the governing issue, or "none";
- `labels_valid`: true/false, with the exact taxonomy deviation if false;
- `gate`: GO or BLOCKED, blocked only when the governing issue is absent;
- proposed corrections or GitHub writes not performed because authorization was absent.

## References

Paths below are repository-relative.

- `AGENTS.md`: skill registration and workflow gates
- `.agents/skills/pr-governance/SKILL.md`: PR linkage and readiness
- GitHub label inventory: `gh label list --repo giulianotesta7/up`
