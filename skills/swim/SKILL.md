---
name: swim
description: Plan and write operational work for swim (swimlane), the operator-in-the-loop runner. Claude writes lane scripts, a person runs them, and Claude reads the logs back. Use when a repo has lane.N.sh / lanes.<name>.sh / swim.yml / .swim/, when the user mentions swim, lanes, rounds, guards, approvals, the queue or `swim watch`, or when operational work (infra changes, migrations, cutovers, prod diagnostics) has to run somewhere Claude can't reach (production, a VPN, the user's credentials).
---

# swim

swim runs AI-written operational work safely. **You write the round, the
operator runs it, the log reports.** You plan and write lane scripts. A
person (the *operator*) runs them with their own access. You read the
results back from files and decide the next round.

## 1. Get the guide from the binary first

The installed swim is the source of truth, and its guide is written for
you. Run these first, once per session:

```sh
swim --version
swim report --earmark --name "${SWIM_SESSION:-claude}"   # what changed since this session last looked
swim status                                              # every lane: idle, pending, running, passed, failed
```

When you need one section of the guide, extract it instead of reading
all of it (headings are uppercase, at column 0):

```sh
swim --help | awk '/^PLANNER WORKFLOW$/{p=1;print;next} p&&/^[A-Z][A-Z ]+$/{exit} p'
```

| Before you... | Read section |
|---|---|
| plan a round | `PLANNER WORKFLOW`, `CONCEPTS` |
| are a subagent writing one lane | `WORKER RULES` |
| write or edit a lane script | `WRITING A LANE SCRIPT`, `SAFETY RULES`, `SYNTAX` |
| write a lane in Python/TS/Go | `OTHER LANGUAGES` |
| read results | `READING RESULTS` |
| touch approvals or the chain | `EXECUTION MANIFEST`, `CHAIN OF CUSTODY` |

`swim <command> --help` gives one command's detail. If this skill and the
binary disagree, the binary wins. Say so to the user.

## 2. What you run, and what you hand over

**Run freely.** These only read or check:
`swim status`, `report`, `log`, `plan`, `plan --check`, `lint`, `timeline`,
`history`, `verify`, `env`, `keys`, `approve` (no arguments: lists them),
`queue` / `queue show`, `schedule list`, `doctor` (without `--fix`),
`changelog`, `--help`. Add `--yaml` when you're going to parse the output.

**Bookkeeping you may do** when it's your round: `swim new`, `swim archive`,
`swim stub`, `swim note`. These touch only lane files and logs, and they
refuse to touch a running lane.

**Hand to the operator; never do these yourself:**
- Running rounds that touch anything outside the repo: `swim run`, `swim
  all`, `swim N`, `swim watch`, `swim ci`. The operator's environment is the
  point. If the user explicitly asks you to run one, do it only for lanes
  with no guards that change nothing outside the repo (lint, tests, local
  builds), and say that's what you're doing.
- **Approving anything.** Never set a guard flag (`FLAG=1 swim run`), never
  pass `-y`, `--approve` or `--auto`, never run `swim approve` or `swim
  revoke`. Approval is a person's decision, and the record attributes it to
  them.
- `swim queue add`, `swim schedule`, `swim interrupt`, `swim secret ...`,
  `swim keys trust`, `swim chain ...`, `swim doctor --fix`, `swim config` /
  config edits.

**Never edit by hand:** `.swim/` (status, logs, chain, runs, queue),
`.swim.log`, `approvals/`, `lookup.yml`, `trust.yml`, the user's
`~/.config/swim/`. Not even to "fix" one. Give the operator the exact
lines or command instead. For anything broken, point to `docs/recovery.md`
in the swimlane repo.

## 3. The loop

1. **Catch up:** `swim report --earmark`, then `swim status`. Don't redo or
   contradict what the record says without asking.
2. **Pick lanes:** only idle ones (or unused numbers 1–99). Never a lane
   that's running or holds a round someone may be about to run, and never
   an Always lane (`[*]` in `swim plan`). Confirm with the user when it's
   unclear.
3. **Write:** `swim new N "<goal>"`, then fill it in (see section 4 and
   [reference/lane-recipe.md](reference/lane-recipe.md)). With subagents:
   one lane per worker, and each writes only its own `lane.N.*`.
4. **Check:** `swim lint N` until it's clean, then `swim plan` and read the
   tree, the predicted skips and the guard flags. `swim plan --check` runs
   the read-only stages for real. Run it only if the user says the target is
   reachable from here, which is usually not.
5. **Hand off:** give the operator the exact command, pinned to job ids,
   with each guard flag and what it approves
   ([reference/handoff.md](reference/handoff.md)). Then stop and wait. Don't
   poll with `swim run`.
6. **Read results:** `swim report --earmark` (or `swim log N`, `swim log N
   --yaml`). **PASS only means exit 0.** Check the real outcome in the step
   output: ids, counts, "No changes", the verify stage. `SKIP ... dry run`
   means a guard wasn't approved. Nothing changed.
7. **Close out:** record results where the project tracks them, `swim note
   "<finding>"` for what the next session needs, then `swim archive N --as
   <what-happened>` and `swim stub N "<why idle>"`. Out-of-scope findings go
   on a follow-up list, not into the round.

## 4. Writing a lane: the rules that matter most

- **Stages in order:** `snapshot` (read-only capture), `check` (checks and
  `gate`s), `change` (only behind `guard`), `verify` (prove it worked). End
  with `summary`.
- **Every destructive or irreversible step goes behind its own `guard
  FLAG "<action, date, reason>"`.** List it in the header (`# Guards:`).
  With no approval it's a dry run, which is the safe default. Never default
  a flag on, and never set one in the script: swim refuses that.
- **Gates fail closed.** An empty or unknown answer stops the round before
  any change.
- **Read the deployed state.** Never assume it from code.
- **Rerunnable:** tag what you create with `$SWIM_JOB`, so a rerun tells
  "ours" from "theirs".
- **Declare ordering in the header,** not in prose: `# After: 2 3`,
  `# Locks: name`, `# Includes: lib/x.sh` for anything you `source`.
- **bash 3.2** (macOS): no associative arrays, `mapfile` or `${x,,}`. **No
  `set -e`.** A failed `run` doesn't stop the round; a failed `gate` does.
- **No `git push` / `commit` / `pull`** anywhere in a lane, not even in an
  echo: it's blocked on its text.
- **Secrets:** use `$(swim secret get NAME)`. Never print values, only
  lengths. Never list a secret in `header_env`.
- **Keep output streaming,** so the operator and the live view see
  progress. Don't capture a long command with `out=$(cmd); echo "$out"`.
  Check its output while it streams instead:
  `bash -o pipefail -c 'cmd | awk "{print} /bad/{b=1} END{exit b}"'`.
  Python tools buffer when piped: set `PYTHONUNBUFFERED=1` for them.
- **No Claude, LLM or agent calls in a lane that decide what the round
  does** (branching, gating, retries, approvals). Decisions are made in the
  script you wrote, which the operator approved. An LLM step may only
  produce an artifact (a report, a note) that nothing in swim reads back.
- **Don't edit a lane while it may be running,** because bash reads scripts
  incrementally. `swim status` shows what's running.

## 5. Talking about safety

Be accurate about what swim guarantees. Approvals, signatures and the chain
prove **who decided what** and that **the approved bytes ran**. They don't
make a script safe: an approved script runs with the operator's access. A
guard is a bash function in the lane, so it's advisory. The exception is a
*guarded secret*, which isn't released until the guard is approved. Never
tell the user a change is safe because swim will ask first. The full model
is `docs/trust-model.md` in the swimlane repo.
