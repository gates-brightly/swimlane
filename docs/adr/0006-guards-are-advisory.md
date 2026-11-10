# 0006. Guards are advisory; only blocked commands are enforced

- **Status:** Accepted
- **Date:** 2026-10-24 (ancestry check dropped); git unblocked
  2026-10-30
- **Source:** 0b9aba3b "Drop the process-ancestry check: a same-user lane
  can do what you can"; e78543b4 "blocked commands: swim never pushes,
  commits or pulls"; a22d6583 "update git controls"

## Context

A guard asks the operator before a lane does something destructive. But
it runs in the lane's own process, as the operator: a lane that never
calls it, or runs the command outside it, runs the command anyway. swim
has to say plainly what it enforces and what it only asks.

## Decision

- A guard is the lane's own check. swim makes the question honest (the
  header's words, 0004), the answer the operator's (0007) and the record
  complete (0008), but doesn't stop a lane that skips it. Lint warns
  about a destructive command with no guard around it.
- The one thing enforced outside the script is a repo's list of blocked
  commands, empty by default: refused before the lane starts and again
  at each step.
- swim itself never writes to git. Lanes may run git and gh; one that
  changes a shared remote goes behind a guard, like any other change.

## Alternatives considered

- **Enforce guards by confining lanes** (another OS user, a container, a
  syscall filter): a lane that skips a guard can't do the thing. But a
  lane needs the operator's credentials, VPN and ssh-agent to do its
  work, so confinement means handing them back piecemeal; that's the
  sandbox proposal dropped in 0005.
- **A process-ancestry check** as a security boundary: dropped on
  2026-10-24. A lane runs as the operator and can do what they can, so
  the check claimed more than it gave. Instead each approval is a
  one-time grant to its lane (0007), and verify fails an approval the
  run didn't record.
- **Block git push, commit and pull in every lane** (built 2026-10-09,
  with a git shim on `PATH`): no lane changes history by accident. Removed
  on 2026-10-30: opening a PR or pushing a branch is ordinary operational
  work, and the block made it impossible rather than deliberate.
- **Refuse unguarded destructive commands:** a guard can't be forgotten.
  But "destructive" is a text match; it would refuse safe lanes (`rm` of a
  scratch dir) and miss a command built at run time.

## Consequences

- The docs say the guard is advisory, and that approval proves who
  decided, not that a script is safe.
- Limiting the damage a lane can do (scoped, short-lived credentials; a
  separate OS user) is outside swim.
- Whether a git command is safe is the guard's, and the operator's, call.

## See also

[Trust model](../reference/trust-model.md#authorization),
[safety](../reference/safety.md), [guards](../reference/guards.md).
