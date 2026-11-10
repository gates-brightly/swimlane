# 0012. An approval is used once, by the lane it was given to

- **Status:** Accepted
- **Date:** 2026-10-14
- **Source:** a02e5ea0 "manifest: approve and run one exact operation";
  31034114 "authority: swim run alone decides, records and attests
  approvals"

## Context

A guard runs in the lane's own process, as the operator, so a lane could
claim to be another lane, reuse an answer, or record an approval nobody
gave. An approval meant for one exact operation has to stay with it.

## Decision

- Under `swim run` the launcher decides every guard: from its own
  environment as the run started, a batch's `--approve`, or the
  operator's answer. A flag the lane sets for itself doesn't count, and
  there's no switch that approves every guard.
- Each lane process gets its own approval token, bound to its process
  tree where the OS can tell. The launcher, not the request, says which
  lane, round and manifest ask, and refuses flags the header doesn't
  declare (0003).
- An approval is a one-time id for that lane and guard; the launcher
  writes the approve entry when the lane uses it. A lane can't approve
  itself, use another's id, or use one twice.
- It fails closed: no one to ask (CI, no terminal, `--no-prompt`) is a
  dry run, never a yes.

## Consequences

- The record (0005) says who approved what, and `swim verify` fails an
  approval its run didn't record.
- The guard stays advisory: a lane that never calls `guard` runs the
  command anyway. Lint warns (`destructive`); only `blocked_commands` is
  enforced outside the script.

## See also

[Trust model](../reference/trust-model.md), [guards](../reference/guards.md).
