# Release-note examples

These changes and versions are hypothetical, not implemented features or a roadmap.
Use source evidence, never these bullets, when drafting a real release.
Title lines are release metadata, not headings to repeat in the body.
The examples use readable main-branch documentation links; pin real release links to verified source refs and replace the illustrative comparison range with a real link.

## Fixes-only release

Title: `v0.1.1`

Body:

```markdown
## Fixes

- Keep the selected monitor after the list refreshes.
- Show the failure reason of a monitor that has never been up.

**Full changelog:** `v0.1.0...v0.1.1`

[Documentation](https://github.com/giulianotesta7/up#readme)
```

## Features and fixes

Title: `v0.2.0`

Body:

```markdown
## Features

- **Edit monitors:** change the URL, interval, timeout or expected status from the TUI.
- **Delete monitors:** remove a monitor you no longer need.

## Fixes

- Keep the selected monitor after the list refreshes.

**Full changelog:** `v0.1.0...v0.2.0`

[Documentation](https://github.com/giulianotesta7/up#readme)
```

For a first release, introduce verified product capabilities and include Getting started.
Do not invent a previous release or a comparison range.
For later releases, omit Getting started unless installation instructions change.
Put required upgrade actions in Upgrade notes before Features/Fixes.
Do not add Highlights merely to repeat Features, or an empty category.
