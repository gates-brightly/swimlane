# swim review

*This review applies Mitchell Hashimoto's publicly known engineering values (workflow over technology, developer UX, strong defaults, crisp output, no flaky tests, ship small and polish). It is not a prediction of what he would say.*

## Status

Rechecked on 2026-10-29 against `649d340`. Fixed and removed: 3 (lint
passes unparseable scripts), 4 (UNSIGNED vs BROKEN), 5 (the mismatch hint
suggests the bypass), 6 (usage errors), 7 (output polish), 8 (`--help` on
a pipe), 1 (the tamper story, now tested), and most of 2 and 10. What's left is below, under the original numbers.

## Open findings

### 2. A missing file at runtime is still a bare ENOENT

`swim lint` now flags a repo file that `# Includes:` doesn't list
(`workspace-file`), and `--fix` adds it. A lane run without linting still
fails with no hint that the staged copy is the cause:

```
[6] cat: deploy.yaml: No such file or directory
[6] bash: scripts/helper.sh: No such file or directory
```

**Smallest fix.** When a staged step fails, look for a missing path that
exists in the repo and print one line: `deploy.yaml exists in the repo but
isn't in # Includes: (staged copy)`.

### 9. Too many concepts, and two of them reuse words

Not started.

- **run** is the command (`swim run`), the run id (`r-20261028T...`), and
  the in-lane step function (`run "say hello" echo ...`,
  internal/assets/lib.sh:309).
- **stage** is a script phase (`stage check`) and also the workspace (the
  "staged copy").

**Smallest fix.** Rename the in-lane `run` to `step` (it already wraps
`swim step`). Call the workspace "the approved copy". In T23 sessions,
record which trust files anyone actually touched.

### 10. Test reliability is not yet proven in CI

The ban on `time.Sleep` in the test rigs is in (`34b0190`), and the e2e
suite waits on events, not time. T29 is still open at P0: three green
pushes on both OSes, plus 20 runs in a row at `GOMAXPROCS=2`.

## What's genuinely good

- **Fast.** A one-step lane runs in 36ms, the binary builds in 0.6s, and unit tests take 5s.
- **The TTY help is a model front door:** grouped, 39 lines, with the core workflow first.
- **The docs are tested.** The README quickstart and docs/quickstart.md run as written in e2e (internal/e2e/quickstart_test.go:44, :71).
- **Several errors name the fix:**
  - `swim approve 2 --guards NOPE` prints `lane.2.sh has no guard NOPE (its guards: HELLO_ALLOW_WRITE)`.
  - `swim new 1 again` refuses with the existing round's name and `--force`.
  - The dry-run line names both hashes (`approved 10bc7d76de5e, running 46a3cf670e7b`).
  - A failing gate shows the command, its stderr, `STOP gate failed`, and the reason again in the summary.
- **`swim lint` catches the template placeholder** that `swim new` leaves behind (`[placeholder]`). It's a good default: you can't ship the scaffold by accident.
- **The trust model is honest.** docs/trust-model.md opens with what swim does *not* protect against and maps each claim to a test.
- **Ruthless cutting is already underway.** The CHANGELOG's Unreleased section removes hub, schedule, plan, pause, modules, the SDKs and more, and `features/` is frozen pending outside users. Keep going.
