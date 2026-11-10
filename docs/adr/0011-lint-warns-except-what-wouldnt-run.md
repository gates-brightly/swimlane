# 0011. Lint warns, except about what wouldn't run as it reads

- **Status:** Accepted
- **Date:** 2026-11-04
- **Source:** a0cfae9 "Lint refuses the steps that wouldn't run as they
  read"; e0c9731 "header-key typos and unreadable time limits are
  errors"; b4f740c "Lint stops refusing valid lanes"

## Context

Lint sees every lane before it runs, and `swim run` refuses a lane with
a lint error. Lint's checks are of two kinds: whether swim reads the
script the way a person does, and whether the script is a good idea.
Only the first is something swim can know.

## Decision

- An error is a form swim would run differently from how it reads: a
  header key it would drop, a time limit it can't read, a step whose
  option or redirect wouldn't apply. The lane doesn't run.
- Everything else is a warning: a destructive command with no guard,
  bash 4 or GNU-only forms, a staged copy missing files. The lane runs;
  `--strict` makes warnings fail where a repo wants that, as this
  repo's CI does.
- A finding is accepted in the script, with a reason, next to the line.

## Alternatives considered

- **Refuse on every finding:** lanes are as safe as lint can make them.
  But warnings are guesses from text: they'd refuse a scratch-dir `rm`,
  a lane meant for a Linux box, a tool that reads only what was pinned.
  The operator would learn to suppress them, and the agent would write
  around them.
- **Warn on everything, refuse nothing:** lint never blocks work. But a
  lane whose header key swim silently dropped runs without its limit or
  its dependency, which is worse than not running.
- **Errors for the worst risks** (unguarded deletes): a backstop. The
  guard is advisory by decision (0006); a text match can't tell a safe
  delete from a dangerous one.

## Consequences

- A lint error is a bug in the script, never a judgment call, so it's
  fixed, not argued.
- A new check starts as a warning unless it's about how swim reads the
  script; promoting one means showing the form runs other than it reads.
- A test runs each refused form through lint and the runtime, so the two
  can't drift.

## See also

[Lanes: lint](../reference/lanes.md#lint).
