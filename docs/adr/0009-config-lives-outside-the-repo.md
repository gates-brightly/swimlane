# 0009. Config lives outside the repo, by repo and branch

- **Status:** Accepted
- **Date:** 2026-11-05
- **Source:** 9d10030 "Config lives in the home directory, keyed by repo
  name and branch; swim.yml is gone"

## Context

Some settings limit what a lane may do: blocked commands (0006), which
secrets are masked, whether the chain must be signed. Everything in the
repo's checkout is in reach of the agent, its workers and the lanes,
which are the things those settings limit.

## Decision

- All config is in the operator's home directory; nothing in a repo
  configures swim.
- It's layered: defaults, then a repo, then a repo's branch. A repo is
  named by its remote, so every worktree of it shares one section.
- Agents never edit it; they give the operator the exact lines to add.

## Alternatives considered

- **A config file in the repo** (`swim.yml`, removed 2026-11-05): settings
  travel with a clone. But any lane or agent can edit it, so a lane could
  unblock its own command or unmask a secret; the limits would be in
  reach of what they limit.
- **A repo file, pinned by hash** (manifests pinned `swim.yml`'s policy
  keys, so a change voided approvals): edits are caught. But every
  agent edit becomes another approval to read, and the file still says
  what the agent wanted, not what the operator chose.
- **Config per checkout path:** simple. But worktrees and fresh clones
  each start from nothing, and an agent's worktree is a new path every
  time.

## Consequences

- A setting can differ per repo and branch without a file in either.
- Settings don't travel with a clone: each operator, and each CI job, has
  their own.

## See also

[Config](../reference/config.md).
