# 0008. swim is a personal tool

- **Status:** Accepted
- **Date:** 2026-11-04
- **Source:** the maintainer's direction of 2026-11-04, and the cleanup
  of 2026-11-05 that followed it

## Context

Reviews kept pulling swim toward adoption: sessions for outside
engineers, a landing page, beginner vocabulary, isolation backends, team
features. Each adds surface that its one regular operator doesn't use,
and a review found thousands of lines with no recorded use.

## Decision

- swim is an opinionated tool built to make its maintainer's own loop
  faster, safer and cheaper in tokens. Changes are judged by that, not
  by how approachable they make swim.
- Cut what isn't used, measured from real use (`.swim.log`, sessions),
  over adding generality. Every two ways of doing one thing get one
  recommended path, and one word per thing.
- The exception: a small, tested day-zero path for a new engineer (the
  README's first five minutes, and four ideas: lane, round, guard, log).
- The shipped skill stays generic; one person's own setup goes in their
  own agent instructions, not the repo.

## Consequences

- Features others might want are declined, or built outside swim
  (0009).
- Removals are routine; the CHANGELOG says what to change.

## See also

[Why swim](../reference/why.md).
