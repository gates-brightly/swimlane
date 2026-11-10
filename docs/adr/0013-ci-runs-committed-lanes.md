# 0013. CI runs committed lanes through swim ci

- **Status:** Accepted
- **Date:** 2026-10-09 (lanes in place of group files: 2026-10-24)
- **Source:** c3aa2888 "swim ci: CI mode with groups, annotations, job
  summary, JUnit and --changed"; 0b9aba3b (group files removed)

## Context

Lane scripts are working files, git-ignored by default (0010). A repo
that wants the same checks on every push needs them in git, and CI has
no operator: no terminal, no approvals, no config of its own (0007).

## Decision

- A repo's CI is lanes it commits (`!/lane.1.sh` in `.gitignore`), run by
  `swim ci`: `swim all` that never prompts, folds each lane's log, writes
  annotations, a job summary and a JUnit report. `--changed` runs only
  rounds whose script changed, and the lanes they wait on.
- A dry-run round counts as a pass in CI (it can't approve); guard flags
  come from the job's environment.
- swim never commits results back.
- This repo's own CI is `swim ci` over its committed `lane.*.sh`.

## Consequences

- The same lanes run locally (`just ci`, `swim run 8`) and in CI.
- Committed lanes are special elsewhere: `swim doctor` accepts tracked
  lane scripts only where CI runs `swim ci`; archive stubs a tracked
  script instead of removing it, and the "passed, not archived" reminder
  leaves them out.

## See also

[CI](../reference/ci.md).
