# 0004. The header is the lane's contract

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
header, or be built at run time.

## Decision

- What a lane may do is what its header declares: the guards it may ask,
  what each does and why, and the repo files it reads.
- A guard the body asks without declaring is refused; a repo file it
  reads without listing stops the round.
- The operator is asked in the header's words, whatever the body sends.

## Alternatives considered

- **Infer from the body** (scan for guard calls and `source` lines): no
  header to keep up. Tried, and it's why this record exists: regexes
  misread bash, and a flag or path built at run time is invisible to
  them, so what was shown and what ran could differ.
- **Parse the body properly** (a bash parser): fewer misreads. Still
  can't see what's computed at run time, and makes swim's contract
  whatever its parser understands.
- **The body declares at run time** (guard text from the call): one place
  to write it. But the words would be known only once the lane runs,
  after the operator was meant to have read them.

## Consequences

- Status can say what a ready round will ask before it runs.
- Lint, not run time, catches a body that outgrows its header, and can
  add the missing lines for a person to review.
- A guard's header line has to be kept in step with the body; lint warns
  when they differ.

## See also

[Guards](../reference/guards.md), [lanes](../reference/lanes.md),
[manifest](../reference/manifest.md).
