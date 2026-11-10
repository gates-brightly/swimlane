# 0005. Lanes run from a pinned copy

- **Status:** Accepted
- **Date:** 2026-10-24
- **Source:** 0b9aba3b "Address the product review: staged workspace,
  strict verify, cuts"; the sandboxed-lanes proposal (2ed9729), dropped
  in 19f07c3

## Context

An approval is worth only as much as the link between what was approved
and what ran. A lane that runs in the working tree reads files that can
change after it was approved or queued: the script, a module it sources,
a config file. The agent keeps editing that tree while lanes wait.

## Decision

- Before a lane waits for anything, swim hashes what it will run (the
  script and the files its header lists), copies exactly those files
  aside, and runs the lane in the copy.
- Approvals and queued batches name that hash; a lane whose files changed
  since is refused, not run.
- A lane can opt out and run in the live tree (for a tool that reads the
  whole directory); the opt-out is part of what's hashed, and lint warns.

## Alternatives considered

- **Hash and trust:** hash the files, check before starting, run in the
  working tree. No copy. But the files can change between the check and
  each read the lane makes, and the agent is editing that tree all the
  while; the bytes approved wouldn't be the bytes that ran.
- **A git worktree at a commit:** a full, consistent tree. But lanes and
  the edits they rely on are mostly uncommitted and git-ignored, so the
  worktree would need them applied on top, plus a checkout per round;
  and pinning a whole tree means hashing, and approving, far more than
  the lane reads.
- **A container per lane** (the sandboxed-lanes proposal, with network
  allowlists): isolation as well as pinning. Dropped on 2026-10-29: it
  needs Docker on the operator's machine, hides the lane's process tree
  that approvals are bound to, and lanes need the operator's VPN,
  ssh-agent and credentials, which a container would have to be given
  back.

## Consequences

- A staged lane sees only what it pinned: a tool run over the whole
  directory finds an almost empty copy (lint warns).
- Files a staged lane writes land in its copy, not the repo.
- Copies accumulate until doctor prunes them.

## See also

[Manifest](../reference/manifest.md), [running](../reference/running.md),
[trust model](../reference/trust-model.md#integrity).
