# 0014. swim never writes to git; lanes may, behind a guard

- **Status:** Accepted
- **Date:** 2026-10-30 (until then, lanes couldn't run git push, commit
  or pull)
- **Source:** e78543b4 "blocked commands: swim never pushes, commits or
  pulls" (2026-10-09); a22d6583 "update git controls" (2026-10-30)

## Context

swim lives in a checkout: its working files sit next to the code. At
first lanes were barred from `git push`, `commit` and `pull`, enforced
three ways and with a git shim on `PATH`. But a lane that opens a PR or
pushes a branch is ordinary operational work, and the block made it
impossible rather than deliberate.

## Decision

- swim itself never writes to git: it doesn't commit, push or pull, and
  keeps its own state in git-ignored files (`.swim/`, `.swim.log`,
  `lane.N.*`), through the block `swim init` writes to `.gitignore`.
- Lanes may run git and gh. One that changes a shared remote goes behind
  a guard, like any change outside the repo.
- A repo that wants a command refused lists it in `blocked_commands`
  (empty by default): refused before the lane starts, and at each step.

## Consequences

- Nothing swim does shows up in a repo's history unless a person or an
  approved lane puts it there.
- Whether a git command is safe is the guard's, and the operator's, call;
  swim doesn't judge it.

## See also

[Safety](../reference/safety.md), [why swim](../reference/why.md).
