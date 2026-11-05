# Agents

How an AI session (the planner) and its subagents (workers) use swim. You
write rounds; the operator runs them with their access; you read the
results back. Your context is for the task, not for watching runs:
dispatch, work on something else, read only what you need when it's in.

## Two machines

Assume you and the operator are on different machines unless the project
says otherwise:

- **You:** a sandbox that sees only the repo. No cloud credentials, VPN,
  SSH keys or the operator's home: don't probe for them.
- **The operator:** their own machine, often macOS (bash 3.2, BSD tools),
  with their access. Lanes run there; a command that works in your sandbox
  hasn't run on theirs.
- **Only the repo is shared.** `swim status`, `queue`, `log` and `report`
  show the operator's runs; `swim env`, `doctor` and `config` describe your
  machine, not theirs.
- **Need a fact from their side?** Queue a lane of snapshot steps rather
  than asking them to paste output.
- **Never write files their machine runs on its own** (`.git/hooks/`,
  `.envrc`, editor tasks): nothing approves them.

## What you run

- **Freely** (they only read or check): `swim` alone, `status`, `queue`,
  `queue show`, `log`, `report` (from your own earmark), `lint`, `history`,
  `verify`, `env`, `keys`, `doctor` (no `--fix`), `changelog`, `guide`,
  `--help`, `wait`. Add `--yaml` only to parse.
- **Your own bookkeeping:** `swim new`, `archive`, `note`.
- **Never:**
  - Run rounds (`swim run`, `all`, `swim N`, `./lane.N.sh`, `watch`, `ci`).
    If the user explicitly asks, only lanes with no guards that change
    nothing outside the repo, and say so.
  - Approve anything, in any form. Never set a variable named after a
    guard. How the operator approves is theirs: don't
    name it, suggest it, or write steps around it.
  - `swim interrupt`, `chain ...`, `doctor --fix`, `config`.
  - Edit `.swim/`, `.swim.log` or `~/.config/swim/` by hand: give the operator the exact lines.
  - Tell anyone a change is safe because swim will ask first: an approved
    script runs with the operator's access.

## The loop

1. **Catch up, cheapest first:** `swim`, `swim status`, `swim queue`,
   `swim log | tail -20`. Then `swim report --earmark --name NAME` (one name
   per session, or `SWIM_SESSION`): what's new since your place, then it
   moves it. Never report from an earmark you haven't set (the first report
   is everything); set one silently with `>/dev/null`. Don't redo or
   contradict what the record says without asking.
2. **Pick lanes:** idle, or unused numbers (1..99). Never one that's
   running, pending someone's run, queued, or committed (the repo's CI).
3. **Write:** `swim new N "<goal>"`, then the steps in the short form, bash
   unless the work needs another language ([lanes](lanes.md)). Keep the
   header accurate: the operator reads it without you. Order goes in the
   header (`# After:`), never just prose.
4. **Check:** `swim lint N` until clean, then `swim status N` (what it
   waits on, how each guard would be decided).
5. **Hand over** (below), then go on to other work. Never poll. With
   nothing else to do, `swim wait <batch>` (the id `swim queue` printed)
   blocks until that batch ends and exits with its result.
6. **Read results** when the batch is done (`swim wait` returned, or
   `swim queue` says so): `swim status N`. FAIL: `swim log N`, the failed step and those before it. PASS only
   means exit 0: read the evidence (`swim log N | sed -n '/^-- stage
   verify/,$p'`). SKIP (dry run): nothing changed; never a result.
7. **Close out:** record results (with the job id) where the project tracks
   them, `swim note "<finding>"` for the next session, `swim archive N
   "<why idle>" --as <what-happened>` (the log is kept, the lane is free).
   Out-of-scope findings go
   on a follow-up list, never into the round.

Something looks wrong (stuck `running`, an orphaned batch, a round that
stopped mid-change, `swim verify` saying no): read-only commands first,
then give the operator the fix from [recovery](recovery.md), quoting the
lines. Don't run fixes yourself.

## Handing over

**Queue a ready round** (`swim queue N ...`), guards and all, without
asking: queueing runs and approves nothing. The swimmer asks the operator
for each guard nothing approved; declined, it's a dry run. With no swimmer
up (`swim queue` says), give the exact command, pinned to the jobs: `swim
run 3f2a9c1e 7b4d4e2a`, with nothing that approves.

Flag every risk on its own line marked ⚠ (a change outside the repo,
production or shared targets, a setup lane the batch pulls in, anything
irreversible), and for every guard write this block, from the
script and what its snapshot and check found:

```markdown
**What you're approving**

- ⚠ **`ORDERS_ALLOW_SST_REMOVE`** (swim 2, change stage)
  - Runs: `npx sst remove --stage prod`
  - Target: SST stack `orders-prod` in account 123456789012
  - Scope: the 14 resources the snapshot lists; terraform owns them, data retained
  - Undo: `npx sst deploy --stage prod` from `v2.3.9`
  - If you say no: a dry run; nothing is removed
```

Need something from the operator first? Config or `deps:`: the exact YAML.
A secret: they set it in their environment or `.env.swim`; never ask for
the value.

## Workers

The planner:

- Assigns each worker one lane before it starts, and a session name (the
  worker runs with `SWIM_SESSION=worker-orders`; its `swim new` claims the
  lane, so another session's `swim new` is refused). Puts
  the goal, the exact `# After:` line, its session name and what to report
  in the worker's prompt, and records the assignment with `swim note`.
- Writes the shared parts: cross-lane ordering, `deps:` lines for the
  operator, shared files. Checks every lane together (`swim lint`, `swim
  status`) and queues them as one batch.
- For a shared environment: lanes declare `# Locks: NAME`.

A worker:

- Writes only its own `lane.N.*`. Never `new`, `archive` or edits
  another lane. If its lane isn't free, it stops and tells the planner.
- Puts its name in `# Owner:`, tags what it creates with `$SWIM_JOB`, keeps
  the `# Job:` line `swim new` wrote.
- Sends changes to shared files back as exact lines; a shared-file change
  must leave its other users working.
- Lints, then reports: lane, job id, each guard and what it approves,
  anything the planner must apply. Never runs the lane.

Two independent sessions in one repo: separate earmark names, read each
other's `swim note`s first, leave your own ("session-b: lanes 20-24 for
billing"), and ask the user when unsure whose a lane is.

## Several tasks at once

Keep one entry per task in your task list: its lanes and state (writing,
dispatched `#41 (21: 63bdf4a9)`, fixing, waiting on the operator, done).
Work a task until it's ready, dispatch it, start the next. At each
stopping point, `swim queue` to see what finished, and pick those up
first. When everything is dispatched, `swim wait --any <batch> <batch>`
returns as the first ends (`swim wait` is the one command that blocks on
work; `swim log -f` only shows it). Rules:

- Don't edit a dispatched lane: the batch is pinned, so the edit is
  skipped. `swim queue rm` it first, or wait.
- Batches that pull in the same setup lane run one after another; queue
  lanes that belong together as one batch.
- About as many streams as the swimmer runs at once (`watch_batches`, 4).
- Batches waiting and nothing running: tell the user once that no swimmer
  is up.
- A task is done when it passed and you read the output that shows the
  outcome; a dry-run SKIP isn't done.

## Campaigns

Rounds that build on each other: read the history first (`swim log N
--all`, `swim log <job>`, `swim history N`, `swim log`), archive with names
that can be found (`--as orders-r3-apply-failed`), keep a thread of `swim
note`s with a fixed prefix, put expensive setup in its own lane that later
rounds depend on by job (`# After: <job>`), and
re-read deployed state every round.
