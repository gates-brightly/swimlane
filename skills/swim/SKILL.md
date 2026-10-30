---
name: swim
description: Plan and write operational work for swim (swimlane), the operator-in-the-loop runner. Claude writes lane scripts, a person runs them, and Claude reads the logs back. Use when a repo has lane.N.sh / lanes.<name>.sh / swim.yml / .swim/, when the user mentions swim, lanes, rounds, guards, approvals, the queue or `swim watch`, or when operational work (infra changes, migrations, cutovers, prod diagnostics) has to run somewhere Claude can't reach (production, a VPN, the user's credentials).
---

# swim

swim runs AI-written operational work safely. **You write the round, the
operator runs it, the log reports.** You plan and write lane scripts. A
person (the *operator*) runs them with their own access. You read the
results back from files and decide the next round.

## 0. Is swim here?

Check before anything else:

```sh
command -v swim && swim --version
ls lane.* lanes.*.sh swim.yml .swim 2>/dev/null
```

- **No `swim` on PATH:** say so, and stop there unless the user wants it.
  The install line is `go install
  github.com/gates-brightly/swimlane/cmd/swim@latest`. Don't install it
  yourself without being asked.
- **swim, but the repo has none of those files:** the repo doesn't use swim
  yet. Don't run `swim init` on your own: it edits `.gitignore` and the
  user's config. Propose it when the work fits swim (operational work that
  has to run where you can't), and otherwise stay out of the way.
- **Version:** this skill was written for **swim 0.20261028.1** or newer:
  a dry run ends SKIP (and `swim run` exits 3), `swim status` shows each
  pending lane's header and guard state, lanes run in a staged copy, and
  `swim run` refuses a lane with lint errors. If `swim --version` is older,
  or a command named here is unknown, tell the user. `swim version` shows the
  latest release and its install line, and `swim skill` shows where to get
  the skill that matches the installed swim. If this skill and the binary
  disagree, the binary wins.

## 1. Get the guide from the binary first

The installed swim is the source of truth, and its guide is written for
you. Run these first, once per session:

```sh
swim report --earmark --name "${SWIM_SESSION:-claude}"   # what changed since this session last looked
swim status                                              # every lane: idle, pending, running, passed, failed
```

**An earmark is a bookmark in swim's record.** `swim report --earmark`
prints everything written since your earmark (status changes,
`.swim.log`, and every lane-log line, untruncated), then moves the
earmark to the end. Each reader keeps its own, under `--name` (default
`$SWIM_SESSION`). Give every session and worker a distinct name, or one
reader's report moves the other's place and it misses lines. It never
edits a log. `--earmark --wait` blocks until there's something new.

When you need one section of the guide, extract it instead of reading
all of it (headings are uppercase, at column 0):

```sh
swim guide | awk '/^PLANNER WORKFLOW$/{p=1;print;next} p&&/^[A-Z][A-Z ]+$/{exit} p'
```

| Before you... | Read section |
|---|---|
| plan a round | `PLANNER WORKFLOW`, `CONCEPTS` |
| are a subagent writing one lane | `WORKER RULES` |
| write or edit a lane script | `WRITING A LANE SCRIPT`, `SAFETY RULES`, `SYNTAX` |
| write a lane in Python/TS/Go | `OTHER LANGUAGES` |
| read results | `READING RESULTS` |

`swim <command> --help` gives one command's detail.

## 2. What you run, and what you hand over

**Run freely.** These only read or check:
`swim status`, `report`, `log`, `lint`,
`history`, `verify`, `env`, `keys`,
`queue` / `queue show`, `doctor` (without `--fix`),
`changelog`, `--help`. Add `--yaml` when you're going to parse the output.

**Bookkeeping you may do** when it's your round: `swim new`, `swim archive`,
`swim stub`, `swim note`. These touch only lane files and logs, and they
refuse to touch a running lane.

**Queueing you may do**, when the user asks or the round is ready to hand
over: `swim queue add N ...`, and `swim queue rm` of a batch you queued.
Queue the lanes and nothing else: a guard the operator doesn't approve
dry-runs. Before you queue,
read `swim lint` and `swim status` for the lanes and `swim queue` for the
workers, and **flag every item with meaningful risk**, each on its own
line marked ⚠, saying what it does and what will happen to it:

- a guarded step that changes or destroys something outside the repo
  (apply, delete, migrate, publish, rotate): name the guard and the action;
- production or shared targets, a setup lane the batch pulls in, and
  anything that can't be undone.

**How the operator approves is theirs, and not your concern.** Don't look
into it, name it, suggest a way to approve, or plan around it: write every
guarded step as if it will be asked, hand over or queue the plain command,
describe each guard's risk, and read the results the same way whoever
approved them. Never skip, hold back, split off or reword a step, or a
batch, because of how it might be approved. Never queue a batch whose risk
you haven't read.

**Hand to the operator; never do these yourself:**
- Running rounds that touch anything outside the repo: `swim run`, `swim
  all`, `swim N`, `swim watch`, `swim ci`. The operator's environment is the
  point. If the user explicitly asks you to run one, do it only for lanes
  with no guards that change nothing outside the repo (lint, tests, local
  builds), and say that's what you're doing.
- **Approving or deciding anything,** in any form. Approval is a person's
  decision, and the record attributes it to them. Never set an environment
  variable named after a guard or decision.
- `swim interrupt`, `swim keys trust`,
  `swim chain ...`, `swim doctor --fix`, `swim config` / config edits.

**Never edit by hand:** `.swim/` (status, logs, chain, runs, queue),
`.swim.log`, `approvals/`, `lookup.yml`, `trust.yml`, the user's
`~/.config/swim/`. Not even to "fix" one. Give the operator the exact
lines or command instead. For anything broken, see
[reference/triage.md](reference/triage.md).

## 3. The loop

1. **Catch up:** `swim report --earmark`, then `swim status`. Don't redo or
   contradict what the record says without asking. If this round continues
   earlier ones, read them first (section 5).
2. **Pick lanes:** only idle ones (or unused numbers 1–99). Never a lane
   that's running or holds a round someone may be about to run, and never
   a lane the repo commits (its CI or setup). Confirm with the user when it's
   unclear.
3. **Write:** `swim new N "<goal>"`, then fill it in (see section 6 and
   [reference/lane-recipe.md](reference/lane-recipe.md)). Write lanes in
   bash unless the user asks for another language or the work is much
   easier in one; it calls swim's steps as commands (`swim guide`, OTHER
   LANGUAGES).
   With subagents, follow [reference/workers.md](reference/workers.md):
   the planner assigns each worker exactly one lane number, and nobody
   touches a lane they weren't given.
4. **Check:** `swim lint N` until it's clean (`swim run` refuses a lane
   with any lint error), then read `swim status N`
   before you ask the operator to run anything: it shows the lane's header,
   where each lane it waits on stands (one that hasn't passed skips it),
   and its guards.
5. **Dispatch or hand off:** queue the round (section 2), or give the
   operator the exact `swim run` command, pinned to job ids and with
   nothing that approves, and a **What you're
   approving** block: for each guard, the commands it lets run, the
   target, the scope with the numbers the checks found, whether it can be
   undone, and what happens if they say no (a dry run); for each decision,
   what each choice does ([reference/handoff.md](reference/handoff.md)).
   Never ask for an approval without it. Then
   don't wait: go on to the next task (section 4). Never poll with `swim
   run`.
6. **Read results** at your next stopping point: `swim report --earmark`
   (or `swim log N`, `swim log N --yaml`). **PASS only means exit 0.**
   Check the real outcome in the step output: ids, counts, "No changes",
   the verify stage. A round that ends **SKIP** (`dry run: <step>`) ran but
   a guard wasn't approved: nothing changed, the lanes after it were
   skipped too, and `swim run` exited 3. A dry run is never a result.
7. **Close out:** record results where the project tracks them, `swim note
   "<finding>"` for what the next session needs, then `swim archive N --as
   <what-happened>` and `swim stub N "<why idle>"`. Out-of-scope findings go
   on a follow-up list, not into the round.

**When something looks wrong** (a lane stuck `running`, a failed round that
stopped mid-change, an orphaned batch, `swim verify` saying `no`): start
with [reference/triage.md](reference/triage.md). Read-only first, then tell
the operator what you found and the exact fix to run.

## 4. Concurrent streams: keep working while swim validates

Work several tasks at once. swim runs one task's validation while you write
the next, and you come back to a task when its results are in. Don't sit
idle waiting for a run.

1. **List the tasks.** Write them in your task list before starting, each
   with the lane(s) that will validate it. Give every task its own lanes
   (section 3, step 2): two streams never share a lane.
2. **Work the first task** until it's ready to validate.
3. **Dispatch its validation:** `swim lint N`, then `swim queue add N ...`
   (no approvals; flag risks as section 2 says). Record the batch number
   and job ids against the task: "orders: #41, swim 21 (63bdf4a9)".
4. **Don't wait.** Start the next task.
5. **At each stopping point** (the next task is ready to dispatch, or
   blocked): dispatch what's ready, then run `swim report --earmark` and
   `swim queue` to see what has finished.
6. **Pick finished tasks back up.** For each task whose batch is done, read
   its results (`swim log <job>`; PASS only means exit 0) and either close
   it out or fix it and go back to step 3. Then carry on with the task you
   were on. Repeat until every task is validated and closed out.

The rules that keep streams apart:
- **Don't touch a dispatched lane.** A queued batch is pinned to its
  scripts: an edit makes swim skip that lane rather than run your new
  version. To change a dispatched lane, `swim queue rm` its batch (if it
  hasn't started) and dispatch again, or wait for it to finish.
- **Shared setup takes turns.** Batches that pull in the same setup lane
  (`After: 1`) run one after another, not side by side. Expect that wait,
  or queue lanes that belong together as one batch (`swim queue add 2 3
  4`): one run, so `After:` between them holds.
- **Only block when there's nothing else to do.** Once every task is
  dispatched or waiting on results, `swim report --earmark --wait` blocks
  until something new is written. Use it then, and only then.
- **Keep a few streams open at most,** about as many as the swimmer runs at
  once (`watch_batches`, default 4). More just sit in the queue.
- **No swimmer, no progress.** If `swim queue` shows batches waiting and
  nothing running, check that a swimmer (`swim watch`) is up, and tell the
  user once.
- **A guarded change dispatched without approval is a dry run,** unless
  the operator approves it on `swim watch`'s screen when the lane gets
  there (the screen asks, in yellow, naming the lane). Flag it (⚠), keep
  working, and check later whether the operator approved it.
  Its batch ends `skipped`, not passed. Never treat a dry run as validated.

[reference/streams.md](reference/streams.md) walks through an example and
shows what to keep in the task list.

## 5. Campaigns: rounds that build on each other

Most real work is several rounds: discover, prepare, change, clean up,
often with an expensive setup and a few retries. Each round should start
from what the previous ones left behind.

- **Read the history before you write the next round:**
  - `swim log N --all`: the lane's rounds, archives included.
  - `swim log <job>`: one round by its job id.
  - `swim history N`: who approved and ran what.
  - `swim log`: the project log, including everyone's notes.
- **Name archives so they can be found:** `swim archive N --as
  orders-r3-apply-failed`, not the default job id.
- **Keep a campaign thread in the record:** one `swim note` per decision
  or finding, with a fixed prefix (`swim note "orders cutover: r3 apply
  failed on the IAM role; r4 retries it alone"`). A fresh session finds
  the thread in `swim log`. A plan that should outlive the campaign
  belongs in the project's own docs or tracker. Ask the user where before
  creating a file.
- **Don't redo expensive setup:** put it in its own lane and depend on its
  exact job (`# After: <job>`), and pass results with `# Outputs:` /
  `input`.
- **Each new round re-reads deployed state.** Don't trust the previous
  round's output as current; snapshot again.

## 6. Writing a lane: the rules that matter most

- **Stages in order:** `snapshot` (read-only capture), `check` (checks and
  `gate`s), `change` (only behind `guard`), `verify` (prove it worked). End
  with `summary`.
- **A guard's description is the operator's summary of what they're
  approving:** swim shows it when they approve, in `swim status` and in the
  chain, and it's often all they read. Write one
  line that says the action (the verb and the command's effect), the exact
  target (resource, environment, account), the scope (how many, how much,
  from the snapshot or check that counted it), whether it can be undone
  and how, then the date and the reason:
  `guard USERS_ALLOW_PURGE "delete the 3 @test.example rows from prod
  users (snapshot keeps them; restore with users-restore.sh) 2026-10-29:
  load-test cleanup"`, not `"cleanup"` or `"run the migration"`. The same
  text goes in `# Guards:`. If one line can't say it, the guard covers too
  much: split it.
- **Every destructive or irreversible step goes behind its own `guard
  FLAG "<action, date, reason>"`.** Declare it in the header (`# Guards:
  FLAG  <what it approves>`): swim reads a lane's guards and includes
  from its header only, and refuses one it doesn't declare (a guard
  dry-runs; an unlisted `source` stops the round). `swim lint --fix` adds
  missing header lines.
  With no approval it's a dry run, which is the safe default. Never default
  a flag on, and never set one in the script: swim refuses that.
- **Mark steps that mustn't run twice `--checkpoint`** (`run --checkpoint
  --key create-bucket "create the bucket" aws s3 mb ...`): slow or
  one-time changes whose effect lives outside the repo. When the round
  fails later and is fixed, its next run keeps them (KEPT) and resumes.
  Never on a check or snapshot. Clearing them (`swim run --fresh`,
  `swim checkpoint rm`) is the operator's call.
- **When the operator knows which way to go and you don't, write a
  decision, not a guess.** `decide NAME "<question>" "<choice 1>" "<choice
  2>" ...` (2 to 9 choices, declared in `# Decisions: NAME  <what it
  decides>`) on its own line, then `case $DECISION in 1) ... ;; esac`.
  Write every path; the operator picks one at run time. Unanswered, the
  round ends there SKIP. A decision steers; a destructive step on a branch
  still goes behind `guard`.
- **Gates fail closed.** An empty or unknown answer stops the round before
  any change.
- **Read the deployed state.** Never assume it from code.
- **Rerunnable:** tag what you create with `$SWIM_JOB`, so a rerun tells
  "ours" from "theirs".
- **Declare ordering in the header,** not in prose: `# After: 2 3`,
  `# Locks: name`, `# Includes: lib/x.sh` for anything you `source`.
- **The lane runs in a staged copy** holding only the script and what
  `# Includes:` pins, so what runs is what was approved. A repo file it
  reads but doesn't pin isn't there: pin it, or (rarely, and the approval
  says so) add `# Workspace: live`. Write the lane's own outputs to
  `$SWIM_OUT`; `$SWIM_ROOT` is the repo root, for the files the round acts on.
- **bash 3.2** (macOS): no associative arrays, `mapfile` or `${x,,}`. **No
  `set -e`.** A failed `run` doesn't stop the round; a failed `gate` does.
- **git and gh are allowed** (commit, push, pull, `gh pr create`, `gh api`).
  A step that changes a shared remote (a push, a PR, a merge) goes behind
  its own guard, like any change outside the repo. swim blocks only what
  the repo lists in `blocked_commands` (`swim config` shows them), on the
  text, so not even an echo of one.
- **Secrets:** a lane gets credentials from its environment (the
  operator's, or `.env.swim`). Never print values, only lengths. Never list
  a secret in `header_env`.
- **Check the target before changing it.** Start a change against cloud
  resources with a `gate` that stops on the wrong account (`gate "account"
  -- test "$(aws sts get-caller-identity --query Account --output text)" =
  "$ACCOUNT"`). For Terraform: `snapshot "plan" -- terraform plan -out
  tfplan` in the check stage and `terraform apply tfplan` inside the guard,
  so what's applied is the plan the operator saw.
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

## 7. Talking about safety

Be accurate about what swim guarantees. Approvals, signatures and the chain
prove **who decided what** and that **the approved bytes ran**. They don't
make a script safe: an approved script runs with the operator's access. A
guard is a bash function in the lane, so it's advisory. Never
tell the user a change is safe because swim will ask first. The full model
is `docs/trust-model.md` in the swimlane repo.
