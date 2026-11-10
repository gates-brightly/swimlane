# 0007. An approval is used once, by the lane it was given to

- **Status:** Accepted
- **Date:** 2026-10-14
- **Source:** a02e5ea0 "manifest: approve and run one exact operation";
  31034114 "authority: swim run alone decides, records and attests
  approvals"

## Context

A guard runs in the lane's own process, as the operator (0006), so a lane
could claim to be another lane, reuse an answer, or record an approval
nobody gave. An approval meant for one exact operation has to stay with
it.

## Decision

- The launcher decides every guard: from its own environment as the run
  started, a queued batch's approvals, or the operator's answer. A flag a
  lane sets for itself doesn't count, and nothing approves every guard.
- The launcher, not the request, says which lane, round and pinned files
  ask. An approval is a one-time grant to that lane and guard; the
  launcher records it when the lane uses it.
- It fails closed: with no one to ask, a guard is a dry run, never a yes.

## Alternatives considered

- **Approve by environment variable alone** (`FLAG=1`, as a lane run by
  hand still does): simple and scriptable. But a lane can set its own
  variables, so under the launcher it would approve itself.
- **Standing approvals** (a flag good for every run, or approvals from
  others in a shared file): fewer questions. Removed with the proof
  machinery (0008): an answer that outlives its operation is one nobody
  gave to this script.
- **Default to yes when no one can answer** (CI, an unattended
  watcher): rounds finish without a person. It turns a missing operator
  into an approval.

## Consequences

- The record says who approved what, and verify fails an approval its
  run didn't record.
- CI and unattended runs dry-run every guard they weren't given.

## See also

[Trust model](../reference/trust-model.md), [guards](../reference/guards.md).
