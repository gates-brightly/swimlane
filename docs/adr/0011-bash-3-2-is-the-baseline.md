# 0011. bash 3.2 is the baseline

- **Status:** Accepted
- **Date:** 2026-10-09 (CI on macOS's bash: 2026-10-15)
- **Source:** 47da6255 "swim lint and swim doctor"; b100de05 "ci: pin the
  runner images (ubuntu-24.04, macos-15)"

## Context

Lanes run on the operator's machine (0002), which is often a Mac: bash
3.2 and BSD tools. Agents write lanes in a Linux sandbox with bash 5 and
GNU tools, so a lane that works where it was written can fail where it
runs: `mapfile`, `declare -A`, `${x,,}`, `sed -i` with no suffix, `grep
-P`, `date -d`.

## Decision

- swim's lane library (`internal/assets/lib.sh`) runs on bash 3.2.
- `swim lint` warns on bash 4 features (`bash4`) and GNU-only tool forms
  (`gnu-only`) in lane scripts and the modules they source; `swim doctor`
  names them for the machine it runs on.
- CI runs every change on macOS too, with its own bash 3.2 first on
  `PATH`, on named images.
- A lane that needs more is written in another language (`lane.N.py`,
  `lane.N.ts`, ...) and calls the plain CLI (`swim step`, `swim guard`).

## Consequences

- Lanes and the library give up bash 4 conveniences; lint names the
  portable form.
- A tool form lint doesn't know can still differ between the two
  machines: the verify stage's output is what says it worked.

## See also

[Compatibility](../reference/compatibility.md),
[lanes](../reference/lanes.md), [other languages](../reference/other-languages.md).
