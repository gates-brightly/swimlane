# 0004. Lanes run from a pinned copy

- **Status:** Accepted
- **Date:** 2026-10-24
- **Source:** 0b9aba3b "Address the product review: staged workspace,
  strict verify, cuts"

## Context

An approval is worth only as much as the link between what was approved
and what ran. A lane that runs in the working tree reads files that can
change after it was approved or queued: the script, a module it sources,
a config file.

## Decision

- `swim run` stages each lane into `.swim/runs/<run>/<lane>/`: the script,
  its sidecar and its `# Includes:`, and runs it there.
- A manifest pins those files by hash; approvals and queued batches name
  the manifest, and a lane whose files changed since is refused.
- `# Workspace: live` opts a lane out (for tools that read the whole tree,
  like `go test`), recorded in the manifest, and lint warns.

## Consequences

- A staged lane sees only what it pinned: a tool run over the whole
  directory reads an empty copy (lint warns, `workspace-empty`).
- Files a staged lane writes land in its copy, not the repo.
- Old copies accumulate; `swim doctor --fix` prunes those of no lane's
  latest round after 14 days.

## See also

[Manifest](../reference/manifest.md), [running](../reference/running.md).
