# 0003. bash 3.2 is the baseline

- **Status:** Accepted
- **Date:** 2026-10-09 (CI on macOS's bash: 2026-10-15)
- **Source:** 47da6255 "swim lint and swim doctor"; b100de05 "ci: pin the
  runner images (ubuntu-24.04, macos-15)"

## Context

Lanes run on the operator's machine (0001), which is often a Mac: bash
3.2 and BSD tools. Agents write lanes in a Linux sandbox with bash 5 and
GNU tools, so a lane that works where it was written can fail where it
runs.

## Decision

- swim's lane library, and the lanes written for it, run on macOS's own
  bash 3.2 and BSD tools.
- Lint warns about bash 4 features and GNU-only tool forms; doctor says
  which of them the machine it runs on can't run.
- CI runs every change on macOS too, with its bash 3.2.
- A lane that needs more is written in another language and calls swim's
  plain commands.

## Alternatives considered

- **Require a newer bash** (Homebrew's bash 5): the library and lanes get
  associative arrays and the rest. But every operator installs it, every
  lane's shebang has to find it, and a Mac that's missing it fails at run
  time on the machine the agent can't see.
- **Require GNU tools on the Mac** (coreutils, gnu-sed): one dialect.
  Same cost, and the GNU names (`gsed`) differ from the Linux ones, so a
  lane still isn't portable.
- **Lanes in one portable language only** (Python, or Go): no shell
  dialects. But a lane is mostly commands an operator would type, which
  bash says most plainly; other languages stay available for lanes that
  need them.

## Consequences

- Lanes and the library give up bash 4 conveniences; lint names the
  portable form.
- A tool form lint doesn't know can still differ between the two
  machines: the verify stage's output is what says it worked.

## See also

[Compatibility](../reference/compatibility.md),
[lanes](../reference/lanes.md), [other languages](../reference/other-languages.md).
