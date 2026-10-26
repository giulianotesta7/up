---
name: release-notes
description: "Trigger: release notes, release message, notas de release, mensaje de release. Draft concise, evidence-backed up release copy without publishing."
license: MIT
metadata:
  author: "giulianotesta7"
  version: "1.0"
---

## Activation Contract

Use when drafting or revising release text. Do not use for building binaries, managing tags or publishing artifacts.

## Hard Rules

- Write English unless the owner requests otherwise. Use only the version as the title; do not repeat it as a body heading.
- Describe verified user impact from the selected source. Never invent changes, verification results or roadmap promises.
- Omit empty sections, repeated README content and routine CI/smoke/checksum reports. Do not use en dashes, em dashes or middle dots.
- Never create tags, change releases/labels, push or alter publication workflows. Return a draft; publication needs separate explicit authorization.

## Decision Gates

| Content | Sections |
| --- | --- |
| First release | Short product introduction, Highlights with the main capabilities and Getting started |
| Fixes only | Fixes |
| Features and fixes | Features, then Fixes |
| Noteworthy change needing explanation or a screenshot | Optional Highlights; do not repeat Features |
| Installation changes | Getting started only when instructions changed |
| Required upgrade action or incompatibility | Upgrade notes before the changes |

## Execution Steps

1. Resolve the requested version, exact target and previous published release. Ask if ambiguous. Inspect the source range and merged PRs; for a first release inspect capabilities instead.
2. Use native generated notes as input, not finished copy. Inspect every change, including unlabeled ones, instead of dropping it.
3. Select only applicable sections. Read `assets/examples.md` for hypothetical formatting, never factual claims.
4. Finish with a real Full changelog comparison when a previous release exists, then one Documentation link line. Pin links to verified source refs. Do not add a Documentation section. up has no Spanish documentation, so there is no Español link.

## Output Contract

Return the version-only title and Markdown body, plus a separate private evidence summary: target/range, change sources and unresolved facts or links. Keep that summary out of the public text. Never claim publication.

Once the owner approves the body, it is stored as `.github/release-notes/<version>.md`; the release workflow publishes that file for the pushed tag and falls back to generated notes when it is missing.

## References

- [Formatting examples](assets/examples.md)
