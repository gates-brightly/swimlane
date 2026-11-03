# UX review: make swim smaller

A plan built from a review of swim's UX and engineering practice (the lens:
plan/apply, the HashiCorp Tao's "simple, modular, composable", Ghostty's
polish and zero-config defaults). The verdict was: *the core idea is
good and the engineering is careful. Now make it smaller: fewer nouns, one
way to do each thing, a lane that's just its steps, and help text that
sells the workflow you actually use.*

**How to use this file:** walk each task, tick one decision box, and write
any changes under **Notes**. Approved tasks not done here become `dev/tasks/T31`…`T39` in
the usual Today / Do / Done when format; rejected ones are deleted from
here. When every row in the table has a decision, this file is done:
delete it.

## Decisions at a glance

| # | Task | Phase | Effort | Risk | Decision |
|---|---|---|---|---|---|
| 1 | [Make the help text match how you use swim](#1-make-the-help-text-match-how-you-use-swim) | 1 | S | Low | Done (`ux-smaller`) |
| 2 | [`swim run` doesn't write files the repo should commit](#2-swim-run-doesnt-write-files-the-repo-should-commit) | 1 | S | Low | Done (`ux-smaller`) |
| 3 | [Clean up the repo root](#3-clean-up-the-repo-root) | 1 | S | Low | Done (`ux-smaller`) |
| 4 | [One spelling per action](#4-one-spelling-per-action) | 2 | M | Low | Done with changes: queue spelling only; flags kept |
| 5 | [Decide which concepts a beginner sees](#5-decide-which-concepts-a-beginner-sees) | 2 | M | Low | Done (`ux-template`) |
| 6 | [A shorter template](#6-a-shorter-template) | 2 | S | Low | Done (`ux-template`) |
| 7 | [Let `swim run` supply the boilerplate](#7-let-swim-run-supply-the-boilerplate) | 3 | L | **High** | Done (`ux-template`): opt-in by `#!/usr/bin/env swim` |
| 8 | [Freeze the format and the versioning](#8-freeze-the-format-and-the-versioning) | 3 | S | Low | Done with changes: changelog kept whole |
| 9 | [Put new work into the queue and log loop](#9-put-new-work-into-the-queue-and-log-loop) | rule | — | — | Adopted: T31–T33 filed |

Suggested order: 1 → 2 → 3, then 6 → 4 → 5, then 7 → 8. Item 9 is a rule
for what to build next, not a task.

---

## Phase 1: quick fixes

### 1. Make the help text match how you use swim

**Why:** the README now says swim is an operator-in-the-loop tool, but
`swim --help` still makes the platform pitch. The queue and the swimmer,
the parts that make swim worth using, are listed under UNATTENDED, below
`approve`, `revoke`, `verify` and `history`.

**Today:** `internal/help/help.go:1316` opens with *"Your AI writes the
ops change. You sign the exact bytes. swim runs it and gives you a receipt
anyone can verify."* The CORE group mixes running lanes with keeping the
record. `swim --help` is 40 lines.

**Do:**
- Open with the README's line: *"An operator-in-the-loop runner for ops
  work. Your AI writes the scripts. You run them, with your access."*
- Group commands by the workflow:
  - **WRITE:** new, lint
  - **RUN:** run, all, queue, watch, wait
  - **READ:** status, log, report
  - **APPROVE:** approve, revoke
  - **RECORD:** verify, history, chain, keys
- Keep MORE for the rest.

**Done when:** `swim --help` fits on one 40-line screen, `queue` and
`watch` are in the first group a reader reaches, and the help tests in
`internal/help` pass with the new grouping.

**Decision:**
- [x] Approve
- [ ] Approve with changes
- [ ] Reject

**Notes:**

Done on `ux-smaller`. `swim --help` opens with "an operator-in-the-loop
runner for ops work" and groups WRITE, RUN, READ, APPROVE, RECORD, MORE;
exactly 40 lines. `wait` moved to MORE and RECORD is one line to fit.
`swim N` shorthand dropped from the overview (still in `swim run --help`).
`TestHelp` checks for WRITE and RUN instead of CORE.

---

### 2. `swim run` doesn't write files the repo should commit

**Why:** a command that runs a lane shouldn't create files you're meant
to commit. A clean error is easier to understand than a file appearing
without warning.

**Today:** `requireVersion` (`internal/cli/cli.go:419`) calls
`version.Ensure` when a job is about to run. In a repo with no
`.swim.lock`, `swim run 1` writes it and prints `swim: wrote .swim.lock
...; commit it so everyone runs a compatible swim`, even when the
repo was never set up (`swim init` was never run).

**Do:**
- With no `.swim.lock`, `swim run`, `swim all`, `swim queue` and `swim
  watch` stop with `swim: this repo isn't set up; run swim init` and exit 2.
- `swim init` is the only command that writes the lock.
- Check CI: `swim ci` on a fresh clone has the committed lock, so it's
  unaffected. Confirm that, and decide whether `swim ci` keeps creating one.

**Done when:** an e2e test runs `swim run 1` in a repo where `swim init`
was never run, sees exit 2 and the message, and `git status` shows no new
files.

**Decision:**
- [ ] Approve
- [x] Approve with changes
- [ ] Reject

**Notes:**

Done on `ux-smaller`. `swim init` writes the lock (safe to rerun);
`run`, `all`, `watch` and `bash lane.N.sh` refuse with `this repo isn't
set up for swim (no .swim.lock): run swim init`, exit 2. **Change:**
`swim ci` still writes a lock (its checkout is thrown away), and `swim
queue` isn't gated: a batch can't run until `watch` can. `swim doctor`
warns of a missing lock. `TestVersionAndLock` covers the refusal and that
nothing is written; `TestDoctor`, `TestQueueRefusesBatchTheWorkerCantRun`
and the doctor unit test were updated (they relied on the old behaviour;
they pass on `main`). CHANGELOG: an Upgrading note.

---

### 3. Clean up the repo root

**Why:** the GitHub page shows working notes before it shows the product.

**Today:** the root has `feedback.md`, `tasks.md`, `tasks/`, `lookup.yml`
and seven `lane.*.sh` files. The lanes are swim's own CI (`swim.yml`
says so, and `.github/workflows/test.yml:56` runs `swim ci`), so they
**stay**: they show swim testing itself.

**Do:**
- Move `feedback.md`, `tasks.md` and `tasks/` under `dev/` (update the
  links in `tasks.md` and anything that points at them).
- Add one line to the README, *"swim's own CI is the `lane.*.sh` files in
  this repo, run by `swim ci`"*, so the lanes read as a showcase rather
  than clutter.
- Leave `lookup.yml` (it's the repo's approver list) and the lanes where
  they are.

**Done when:** the root holds the product, its config and its CI lanes;
`go test ./internal/docs/` passes (links resolve).

**Decision:**
- [x] Approve
- [ ] Approve with changes
- [ ] Reject

**Notes:**

Done on `ux-smaller`. `tasks.md`, `tasks/` and `feedback.md` are in
`dev/`; links fixed in `features/README.md`, `docs/trust-model.md`,
`docs/usage.md` and the e2e test comments. The README's "Where next" says
the root `lane.*.sh` files are swim's own CI.

---

## Phase 2: fewer ways to do the same thing

### 4. One spelling per action

**Why:** each alias and flag made sense when it was added, but together
they add up to a lot to learn. One way to do each thing, and detect
what can be detected.

**Today:**
- `swim queue 2 3` and `swim queue add 2 3` (`internal/cli/queue.go:41`)
- `swim 1` and `swim run 1`
- `swim run` takes `--plain`, `--no-tui`, `--no-prompt`, `-y`
  (`internal/cli/commands.go:390`); `swim watch` takes `--no-tui`, `-y`,
  `--approved` (`internal/cli/queue.go:731`)

**Do:**
- Document `swim queue 2 3`. `queue add` keeps working and leaves help.
- Document `swim run 1`. `swim 1` keeps working as a shorthand, mentioned
  once.
- No TTY means plain output and no prompts, automatically. Keep
  `--no-tui` as the one explicit override and `-y` as the bypass.
  `--plain` and `--no-prompt` become hidden aliases. Nothing breaks.

**Done when:** help shows each action once; old spellings still pass their
existing tests; a test runs `swim run` without a TTY and gets plain output
with no prompt.

**Decision:**
- [ ] Approve
- [x] Approve with changes
- [ ] Reject

**Notes:**

Done on `ux-smaller`, **queue spelling only**. The flag premise was
wrong: swim already turns off colour, the live panel and prompts without
a terminal (`display.go:85,100,105`, `ask.go:14`), and `--plain`,
`--no-tui` and `--no-prompt` each override a different thing *on* a
terminal; the tested quickstart uses two of them. Flags kept as they are.
`swim queue N ...` is the spelling in help, messages, docs, the skill and
the demos; `swim queue add` remains for queueing every pending lane (bare
`swim queue` lists). Internal code comments still say `queue add`.

---

### 5. Decide which concepts a beginner sees

**Why:** using swim fluently means learning about 17 nouns: lane, round,
job, task, batch, swimmer, stage, gate, guard, snapshot, drift, decision,
checkpoint, earmark, signoff, claim, stub. Terraform launched with about
five.

**Today:** the README quickstart, `swim init`'s output and `swim new`'s
template introduce several of them on day one (job ids, stages,
`# After:`, `# Guards:`, `# Timeout:`).

**Do:**
- Write down the day-one nouns: **lane, guard, queue, log** (plus
  *round*, if it can't be avoided).
- Rewrite the quickstart, `init`'s output and the template to use only those.
- Move the rest to a "Going further" section of `swim guide`.

**Done when:** a test in `internal/docs` fails if the quickstart block or
the template uses a noun outside the day-one list.

**Decision:**
- [ ] Approve
- [x] Approve with changes
- [ ] Reject

**Notes:**

Done on `ux-template`, after the template settled (6, 7). **Change:** day
one is six ideas, not four: lane, round, step, guard, queue, log (stage,
run, gate and snapshot are the script's syntax). `swim init` dropped the
signing-key and status-file lines; `swim new` prints one `next:` line (no
job id or pinned run); `swim queue` says "1 lane", leaves out standing
approvals without `lookup.yml`, and says "no swim watch is running yet"
without syntax versions. The README quickstart ends with `swim queue 1 2`
+ `swim watch --once` instead of `verify`/`history`. The guide's CONCEPTS
holds the day-one ideas and GOING FURTHER the rest (new short entries for
batch, checkpoint, drift, chain, claim, earmark, signoff); the guide's
`queue add [N|JOB ...]` line and stale lock line are fixed.
`TestDayOneVocabulary` keeps later words out of the quickstart, the
template and init/new/queue output.

---

### 6. A shorter template

**Why:** the first file swim writes for you is a lot to read before you
can start.

**Today:** `internal/assets/lane.tmpl.sh` is 37 lines: an 11-line comment
block explaining the header, plus empty `# After:`, `# Guards:` and
`# Timeout:` lines, before any step.

**Do:**
- Drop the comment block (`swim lint` and `swim guide` already cover it)
  and the empty header lines. Keep `Round`, `Job` and `Owner`.
- Keep the stage skeleton and the commented-out guard example; that's the
  part people copy.

**Done when:** `swim new` writes about 15 lines, `swim lint` still flags
the placeholders, and `TestQuickstartRuns` passes.

**Decision:**
- [x] Approve
- [ ] Approve with changes
- [ ] Reject

**Notes:**

Done on `ux-template`, together with 7: `swim new` writes the short form
in 21 lines (was 37): header (Round, Job, Owner), the four stages and the
guard example, with a two-line note on `# Guards:`. The comment block,
the empty `After`/`Guards`/`Timeout`/`Created` lines and the library
boilerplate are gone.

---

## Phase 3: the format change

### 7. Let `swim run` supply the boilerplate

**Why:** every bash lane repeats the same lines: a `# swim: syntax 4`
line, an `eval "$(swim lib)"` block, `lane_init N` (the number is already
in the filename) and `summary`. A lane should be just its header and its
steps.

**Today:** `lane_init` (`internal/assets/lib.sh:27`) and `summary`
(`lib.sh:500`) are called by the script itself. Lanes in other languages
already call `swim run|gate|...` as commands.

**Do:**
- **Additive, within syntax 4.** If a bash lane has no `lane_init` line,
  `swim run` wraps it: load the library, `lane_init N` from the filename,
  run the steps, `summary`. Lanes with the explicit form run unchanged.
- The short form:
  ```bash
  #!/usr/bin/env swim
  # Round: Say hello
  stage check
  run "say hello" echo "hello from swim"
  ```
- Running the file with plain `bash lane.N.sh` needs the explicit form;
  lint says which form a lane uses.

**Risk (high):** approvals bind the execution manifest, which includes how
a lane runs. The wrapper must be part of the manifest so approving a
short-form lane approves the wrapper too, and approvals people already
hold for explicit-form lanes must keep matching. Get this wrong and
approvals silently stop counting, or count for something they didn't see.

**Done when:** both forms pass the same e2e suite; a manifest test shows a
short-form approval binds the wrapper; an approval on an existing
explicit-form lane still matches after the change.

**Decision:**
- [ ] Approve
- [x] Approve with changes
- [ ] Reject
- [ ] Defer (do 1–6 first and see if it still hurts)

**Notes:**

Done on `ux-template`. **Change:** opt-in, not detection. A bash lane
without `lane_init` already runs inside `swim _wrap` (each `swim run
"x" -- cmd` a step, or the whole program one step), so treating every
such lane as short form would change how existing lanes run and stale
their approvals. The short form is a script whose first line is
`#!/usr/bin/env swim`: `swim run` runs `bash -c <prelude>`, which loads
the library, calls `lane_init N` (N from the file name) and sources the
script; the library's exit trap already ends a round without `summary`.
`./lane.N.sh` runs it as `swim run N` (any directory); `bash lane.N.sh`
can't. Its manifest pins runner `bash (short form)`; explicit lanes still
pin `bash`, and `TestManifestHashFrozen`'s golden hash is unchanged.
Tests: `TestShortFormLane` (dry run, signed approval binds, an edit stales
it), `TestShortFormShebang` (`./lane.2.sh` from another directory, a
failed gate, a queued run), plus unit tests in lane, runner, manifest and
lint. README, hour-long quickstart, guide and skill recipe use it.

---

### 8. Freeze the format and the versioning

**Why:** the lane script format is what people write against, the way HCL
was for Terraform, so it has to stay put. The CLI can keep changing.

**Today:** scripts are at `syntax 4` about a month in. The version scheme
moved from "Breaking version 7" to `0.YYYYMMDD.N`; `go.mod` retracts two
tags; `CHANGELOG.md` is 166 KB.

**Do:**
- Add to `docs/compatibility.md`: *script syntax 4 is stable; new headers
  are additive; no syntax 5 without a written migration.*
- Keep `0.YYYYMMDD.N` as the scheme from here on.
- Make the changelog a short "what changed for you" per release; archive
  the full history in `docs/changelog-archive.md` (or rely on git).

**Done when:** the compatibility page states the freeze, and the newest
changelog entry fits on one screen.

**Decision:**
- [ ] Approve
- [x] Approve with changes
- [ ] Reject

**Notes:**

Done on `ux-template`. `docs/compatibility.md` freezes script syntax 4
(additive only; no syntax 5 without a written migration) and keeps the
`0.YYYYMMDD.N` scheme; it also fixes "the first run writes the lock" (item
2 made that wrong). **Change:** the changelog isn't archived: it's
embedded in the binary and `swim changelog --since <old version>` prints
upgrade notes from it, so cutting old releases would drop them. Instead,
from the next release each opens with a ten-line **What changed for you**
(rule in the CHANGELOG header).

---

## A rule, not a task

### 9. Put new work into the queue and log loop

**Why:** the queue, the dependency graph and the log the AI reads back are
what make swim faster; the trust features already go well past what a
solo operator needs.

**Rule:** for the next month, no new trust levels, anchors or verify modes.
Candidate work instead:
- `swim watch` shows *why* a batch is waiting (which lane, which lock).
- `swim log N --since-fail`: only the part of a log the AI needs after
  a failure.
- Re-queue a failed batch from where it stopped.

**Decision:**
- [x] Adopt the rule
- [ ] Adopt with changes
- [ ] Reject

**Pick the first candidate (if adopted):**
- [ ] `swim watch` shows why a batch waits
- [ ] `swim log N --since-fail`
- [ ] Re-queue a failed batch from where it stopped
- [ ] Other:

**Notes:**

Adopted in `dev/tasks.md` ("Where new work goes", from 2026-11-03 for a
month). The candidates are filed: T31 (`swim watch` says why a batch
waits), T32 (`swim log N --failed`) and T33 (requeue a failed batch from
where it stopped), each with what exists today and its file:line. First
candidate not picked yet.
