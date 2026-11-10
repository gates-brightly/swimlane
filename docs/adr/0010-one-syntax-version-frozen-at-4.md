# 0010. One syntax version, frozen at 4

- **Status:** Accepted
- **Date:** 2026-11-03 (one number for scripts and logs: 2026-11-05)
- **Source:** cb042b1f "One syntax number, and no .swim.lock: swim refuses
  only a newer file"

## Context

Lane scripts are what people and agents write against, and an agent
writes them from what it learned, which may be older than the swim that
runs them. swim had separate version numbers for scripts, logs and
state, a lock file and migrations, and each format change meant upgrade
work for every repo.

## Decision

- One version number, said by lane scripts and logs. swim reads every
  older file as it is and refuses only a newer one.
- Syntax 4 is frozen: new headers, functions and forms are additive, so
  a syntax 4 script that runs today runs on every later swim.
- No syntax 5 without a written migration: what changes, why it couldn't
  be additive, and upgrade notes, before the release that ships it.

## Alternatives considered

- **A version per format, with migrations** (what swim had): each format
  moves at its own pace. But it gave three numbers to keep straight, and
  each bump was work in every repo.
- **A lock file per repo pinning swim's version** (`.swim.lock`, removed
  2026-11-05): a repo says which swim it needs. But it's one more file in
  every repo, and one more thing to upgrade there.
- **Break freely, with upgrade notes:** a cleaner syntax sooner. But
  agents keep writing the old form long after a change, and every break
  lands on the operator at run time.

## Consequences

- Changes are shaped to be additive, which sometimes means two forms of
  one thing; lint steers to the newer one.
- An older swim reading a newer form may refuse or fail safe (a guard it
  can't read isn't approved); forward compatibility isn't promised.

## See also

[Compatibility](../reference/compatibility.md).
