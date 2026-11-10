# 0006. One syntax version, frozen at 4

- **Status:** Accepted
- **Date:** 2026-11-03 (one number for scripts and logs: 2026-11-05)
- **Source:** cb042b1f "One syntax number, and no .swim.lock: swim refuses
  only a newer file"; [compatibility](../reference/compatibility.md)

## Context

Lane scripts are what people and agents write against. swim had
separate version numbers for scripts, logs and state, a lock file
(`.swim.lock`) and migrations, and each format change meant upgrade
work for every repo.

## Decision

- One number, `syntax.Version`, said by lane scripts (`# swim: syntax 4`)
  and logs (`# swim log | syntax 4 | lane N`). swim reads every older
  file as it is and refuses only a newer one.
- Script syntax 4 is frozen: new headers, functions and forms are
  additive, so a syntax 4 script that runs today runs on every later
  swim.
- No syntax 5 without a written migration: what changes, why it couldn't
  be additive, and Upgrading notes, before the release that ships it.

## Consequences

- Changes are shaped to be additive: `guard FLAG` with no text, and the
  `#   why:` line, came in under syntax 4, with the two-argument form
  still running.
- An older swim reading a newer form may refuse or fail safe (a guard it
  can't read isn't approved); forward compatibility isn't promised.

## See also

[Compatibility](../reference/compatibility.md).
