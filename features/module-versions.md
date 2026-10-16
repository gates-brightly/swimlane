# Module version checks: the same module on every machine, or a clear refusal

Status: proposed

## Summary

A lane's modules come from the swim binary that runs it (built-ins) or from
the repo (repo modules). The manifest pins each module's version and hash,
so an approval covers its bytes. But nothing checks, ahead of time, that
the machines involved agree: the planner's swim, the approver's, and the
worker's that runs the batch. Today a mismatch shows up late, as a stale
approval, a lane that won't start, or (with an older swim) a lane that
runs without its modules.

This adds version checks for modules:

- **The repo pins the built-in modules it uses** in `.swim.lock`, with
  their versions and hashes, as it pins syntaxes.
- **`swim modules check`** (and `swim doctor`) compares the lock, the lanes'
  `# Modules:`, and this swim's built-ins, and says what differs and how
  to fix it.
- **Lanes may require a minimum version** (`# Modules: tf@1.2`), not just a
  major.
- **Every machine refuses early and clearly**: `swim plan`, `swim approve`,
  `swim queue add` and `swim watch` say "this swim's built-in tf is 1.1.0;
  the repo uses 1.2.0: update swim" before anything runs.

## Motivation

On 2026-10-16, building and testing modules:

- The Mac ran a swim from before modules. A lane using `mod` ran, every
  module call failed outside a step, and the log didn't say why (T33).
  Script syntax 4 now makes such a swim refuse the lane, but only after a
  `swim lock --upgrade` every repo has to do, and the refusal reached only
  the worker's terminal.
- `go install ...@latest` returned an older release for a while after each
  new tag (the module proxy's cache), so "update swim" didn't update it.
- A built-in module's bytes change with the swim release. A lane approved
  on one machine and run by a worker with another release is refused at
  the step ("this swim's built-in notify isn't the 1.0.0 the manifest
  pinned"), after the batch was queued and claimed.
- `@1` pins a major only. A lane that needs a module feature added in a
  minor version (`tf --lock-timeout`, say) has no way to say so; on an
  older swim the call fails with a usage error mid-round.

## Behaviour

### The lock pins built-in modules

```yaml
# .swim.lock
syntax: {script: 4, log: 2, state: 2}
modules:
  builtin:
    tf: {version: 1.2.0, sha256: 3be1…}
    notify: {version: 1.0.0, sha256: 1531…}
```

- Written by `swim modules lock` (or `swim lock --upgrade`) from the
  built-ins the repo's lanes declare. Committed, so a change is a reviewed
  diff.
- A swim whose built-in differs from the lock's (newer or older) refuses
  lanes that use it, saying which way: "update swim" or "run `swim modules
  lock` to move the repo to tf 1.3.0, and re-approve the lanes that use
  it".
- A built-in the lock doesn't name (a new one) is fine until a lane uses
  it; then `swim lint` says to lock it.

### Minimum versions in the header

- `# Modules: tf@1.2` means 1.2 or later within major 1. `tf@1` stays
  "any 1.x". An exact pin (`tf@=1.2.0`) is allowed but rarely needed: the
  lock already pins exact bytes.
- Resolution refuses a module older than asked for: "tf is 1.1.0 in this
  swim; the lane needs 1.2 or later".

### Checks, early, everywhere

- `swim modules check [--yaml]`: per module, what the lanes ask for, what
  the lock pins, what this swim has, and the fix. Exit 1 on a mismatch.
- `swim doctor` runs the same check (`module-version`).
- `swim plan`, `swim approve` and `swim queue add` refuse, or warn, before a
  mismatched lane is approved or queued.
- `swim watch` checks at start and before claiming each batch: a worker
  that can't run a batch's modules leaves it queued, with the reason in the
  entry and `.swim.log` (T33), rather than failing it.

### `swim version` and the proxy

- `swim version` (which already shows the latest release) says when `go
  install ...@latest` would give an older release than the latest tag, and
  prints the install line with the exact version.

## Design

| Area | Change |
|---|---|
| `internal/version` | The lock's `modules:` section: read, write, compare. A lock without one is valid (no repo has used modules yet). |
| `internal/module` | `@MAJOR.MINOR` minimums in `ParseRef`; resolution against the lock; `Check(root, cfg, lanes) []Mismatch`. |
| `internal/cli` | `swim modules check`, `swim modules lock`; the checks in plan, approve, queue add, watch. |
| `internal/doctor` | `module-version`. |
| `internal/launcher` | Refuse a lane whose modules don't match before staging, with the reason in its status and log. |
| Docs | MODULES in the guide; the lock section; upgrade notes. |

## Open questions

- **Should a lock mismatch on a built-in refuse or warn?** Refusing is
  safe and consistent with syntaxes; warning lets a repo run while one
  machine lags. Leaning: refuse to run, warn in plan.
- **Keep old built-in versions in the binary** for a release or two, so a
  swim can run a lane pinned to the previous `notify`? It softens
  upgrades, at the cost of shipping more code.
- **Repo modules** are pinned by the manifest already; do they belong in the
  lock too? Probably not: they're in the repo, so the commit is the pin.

## Testing

- Unit: `@1.2` resolution against 1.1.0, 1.2.0, 1.9.0 and 2.0.0; the lock
  read and written; `Check` finding each kind of mismatch.
- e2e: a lock pinning another built-in version makes plan say so, run
  refuse with the fix, and `swim modules lock` move it; a worker that
  can't run a batch's modules leaves it queued with the reason.

## Out of scope

- Fetched modules' versions: phase three of [modules.md](modules.md) puts
  them in the same lock section.
- Updating swim for the operator. swim says what to install; installing
  stays theirs.
