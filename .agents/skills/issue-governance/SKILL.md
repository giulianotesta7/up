---
name: issue-governance
description: "Trigger: creating an issue, starting a feature, fixing a bug, labeling, planning work, or any change to up. Issues are optional for every change, recommended for large or design-changing work, never a blocking gate; permitted labels are applied best-effort."
license: MIT
metadata:
  author: "heohlee"
  version: "1.0"
---

## Activation Contract

Use when an up change is about to start, an issue needs creating or triaging, planning transitions to implementation, or existing work needs its governing issue checked.

Do not use for read-only Q&A with no repository change.

## Hard Rules

- An issue is optional for every change and every contributor: a pull request may arrive without one, and no CI check requires it. Recommend opening an issue first only for large or design-changing work, so the idea can be discussed before code is written. Never block, close or delay a change for lacking an issue.
- When an issue exists, it is the intent anchor for the change and the target of the PR's `Closes #<n>` line.
- No issue needs approval. Never require an approval label or invent a separate approval round trip.
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

### Why there is no issue or approval gate

Requiring an issue before every pull request discourages contributions and doubles the moderation work: an issue and a pull request for the same change. The rule is uniform across contributors and change types on purpose: any rule that has to classify the person or the change creates a fresh judgment gate. Do not reintroduce issue or approval requirements through labels, title conventions or contributor roles.

## Decision Gates

| Condition | Result |
| --- | --- |
| No issue exists for the change | GO: the issue is optional; suggest one only for large or design-changing work |
| Issue exists | GO: respect other project skills, with no approval wait |
| Bug, capability or docs issue has missing or conflicting type labels | GO: apply or propose the best-fit single type; never block over labels |
| Chore, CI or tooling issue is unlabeled | GO: this is the intended classification |
| Label is ambiguous or outside the permitted defaults | GO: note or propose a correction; do not create labels |

## Execution Steps

1. Search for a governing issue and reuse it if present. For large or design-changing work, propose one and, when authorized, create it with motivation and acceptance criteria before implementation. Otherwise skip this step.
2. When an issue exists, apply exactly one type for bugs, capabilities or docs-only work. Leave chores, CI and tooling unlabeled. Use triage labels only where meaningful and note uncertainty without waiting.
3. Begin implementation right away, or once the issue exists if you chose to open one. No approval step is needed.
4. Keep classification truthful as scope changes, subject to authorization for remote edits.
5. When an issue exists, link the delivered PR back with `Closes #<n>`, following `pr-governance`.

## Output Contract

Return:

- `issue`: number/URL of the governing issue, or "none";
- `labels_valid`: true/false, with the exact taxonomy deviation if false;
- `gate`: always GO; an absent issue or imperfect labels never block;
- proposed corrections or GitHub writes not performed because authorization was absent.

## References

Paths below are repository-relative.

- `AGENTS.md`: skill registration and workflow gates
- `.agents/skills/pr-governance/SKILL.md`: PR linkage and readiness
- GitHub label inventory: `gh label list --repo heohlee/up`
