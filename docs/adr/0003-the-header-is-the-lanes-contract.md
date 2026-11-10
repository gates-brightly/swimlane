# 0003. The header is the lane's contract

- **Status:** Accepted
- **Date:** 2026-10-16 (guard text from the header: 2026-11-10)
- **Source:** e186f333 "lane: the header is the contract; no regexes
  reading lane scripts"; fa1be50 "guards: say what they do and why, from
  the header"

## Context

swim used to read a lane's body to learn what it may do: which guards it
asks, which files it sources. Reading bash with regexes is unreliable,
and a body can do what a scan misses. The words an operator approved
came from the guard call in the body, so they could drift from the
header, or change at run time.

## Decision

- What a lane may do is what its header (or `lane.N.yml`) declares:
  `# Guards:` (each flag, or `*` for flags named at run time),
  `# Includes:` (the repo files it reads).
- A guard the body uses without declaring it is refused when it asks; a
  repo file it sources without listing it stops the round under `swim
  run`.
- What a guard does and why are its header lines (`# Guards: FLAG
  <does>`, then `#   why: <why>`). Under `swim run` the question is put
  in the pinned header's words, whatever the guard call sends.

## Consequences

- `swim status` can say what a ready round will ask before it runs.
- Lint, not run time, catches a body that outgrows its header;
  `swim lint --fix` adds missing lines for a person to review.
- The body is still read for where its stages are, nothing that grants
  anything.

## See also

[Guards](../reference/guards.md), [lanes](../reference/lanes.md),
[manifest](../reference/manifest.md).
