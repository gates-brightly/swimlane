# Concurrent streams: keep working while swim validates

Work several tasks at once. swim runs one task's validation while you write
the next, and you come back to a task when its results are in. Don't sit
idle waiting for a run.

1. **List the tasks.** Write them in your task list before starting, each
   with the lane(s) that will validate it. Give every task its own lanes
   (the loop, step 2): two streams never share a lane.
2. **Work the first task** until it's ready to validate.
3. **Dispatch its validation:** `swim lint N`, then `swim queue N ...`
   (no approvals; flag risks as SKILL.md's hand-over rules say). Record the
   batch number and job ids against the task: "orders: #41, swim 21
   (63bdf4a9)".
4. **Don't wait.** Start the next task.
5. **At each stopping point** (the next task is ready to dispatch, or
   blocked): dispatch what's ready, then `swim queue` to see what has
   finished. It costs a few lines; a report of every running lane's
   output costs far more.
6. **Pick finished tasks back up.** For each task whose batch is done, read
   its results as the loop's step 6 says (status, then the failed step or
   the verify stage) and either close it out or fix it and go back to step
   3. Then carry on with the task you were on. Repeat until every task is
   validated and closed out.

The rules that keep streams apart:
- **Don't touch a dispatched lane.** A queued batch is pinned to its
  scripts: an edit makes swim skip that lane rather than run your new
  version. To change a dispatched lane, `swim queue rm` its batch (if it
  hasn't started) and dispatch again, or wait for it to finish.
- **Shared setup takes turns.** Batches that pull in the same setup lane
  (`After: 1`) run one after another, not side by side. Expect that wait,
  or queue lanes that belong together as one batch (`swim queue 2 3
  4`): one run, so `After:` between them holds.
- **Only block when there's nothing else to do.** Once every task is
  dispatched or waiting on results, wait for whichever of your batches
  ends first: `swim wait --any '#41' '#42' '#44'` (batch numbers or job
  ids). It returns with that one's result; read it, then wait on the
  rest again. Use it then, and only then. Never `swim queue ... --wait`
  while you have other work: it blocks until that batch ends.
- **Keep a few streams open at most,** about as many as the swimmer runs at
  once (`watch_batches`, default 4). More just sit in the queue.
- **No swimmer, no progress.** If `swim queue` shows batches waiting and
  nothing running, check that a swimmer (`swim watch`) is up, and tell the
  user once.
- **A guarded change is dispatched like any other:** flag it (⚠), keep
  working, and check later what the operator decided. A dry run ends its
  batch `skipped`; never treat it as validated.

What follows is what it looks like in a session with three tasks.

## What the task list holds

One entry per task, kept current. A fresh session, or you at the next
stopping point, should know where every stream is from this alone:

```
1. orders: move the IAM role to terraform    lanes 21   dispatched #41 (21: 63bdf4a9)
2. billing: raise the queue's retention       lanes 22   writing
3. users: drop the legacy index               lanes 23   not started
```

States: **not started**, **writing**, **dispatched** (batch number, and
lane and job id for each lane), **fixing** (the run failed; what's next),
**waiting on the operator** (a guard needs approving), **done**.

## A session

```
# 1. List the tasks (above), each with its own lane.

# 2-3. orders is ready: check it, dispatch its validation, record it.
swim lint 21
swim queue 21                         # → queued #41; job 63bdf4a9
#   task 1: dispatched #41 (21: 63bdf4a9)

# 4. Don't wait: start billing, lane 22.
swim new 22 "billing: raise the queue's retention"
#   ... write lane.22.sh ...

# 5. billing is ready: a stopping point. Dispatch, then look.
swim lint 22
swim queue 22                         # → queued #42
swim queue
#   #41 done: failed. #42 running.

# 6. orders finished: pick it up before going on.
swim status 63bdf4a9                  # FAIL: the gate "role exists in tf state"
swim log 63bdf4a9                     # read that gate and the steps before it: the import step never ran
#   ... fix lane.21.sh (#41 is done, so the lane is free to edit) ...
swim lint 21
swim queue 21                         # → queued #43
#   task 1: dispatched #43 (21: 9c2e71d0), fixing the import order

# Back to step 4: start users, lane 23.
swim new 23 "users: drop the legacy index"
#   ... write lane.23.sh: the drop is behind guard USERS_ALLOW_DROP_INDEX ...
swim lint 23
swim queue 23                         # → queued #44
#   ⚠ swim 23, USERS_ALLOW_DROP_INDEX: drops users_legacy_email_idx in prod.
#     The swimmer's screen asks the operator for it; declined, it dry-runs.
#   task 3: dispatched #44, the operator decides USERS_ALLOW_DROP_INDEX

# Every task is dispatched or waiting: now block, on the first to end.
swim wait --any '#42' '#43' '#44'; swim queue
#   #42 passed, #43 passed, #44 skipped.
swim log 22 | sed -n '/^-- stage verify/,$p'   # billing's evidence (PASS only means exit 0): close it out
swim log 21 | sed -n '/^-- stage verify/,$p'   # orders' evidence: close it out
#   #44 skipped: the operator declined USERS_ALLOW_DROP_INDEX, so its change was a dry run. Not validated yet.
```

## When to look

- **Every stopping point:** run `swim queue`. A few lines say which
  batches finished; read only those lanes (status, then the failed step or
  the verify stage). `swim report --earmark` (from your own earmark) is
  fine when few lanes ran, but it carries every running lane's output.
- **Before editing a lane:** run `swim queue`. Is a batch of yours waiting
  on it? Then `swim queue rm <id>` first, or work on something else.
- **Nothing left to do:** block on the first of your batches to end,
  `swim wait --any '#42' '#43' ...`, then `swim queue`. Not before then.

## Closing a task out

A task is done when its validation passed **and** you've read the output
that shows the outcome: ids, counts, "No changes", the verify stage. A
dry-run SKIP isn't done, and neither is a lane skipped because a
dependency failed. Record the result where the project tracks work,
`swim archive N --as <task>-ok`, and `swim stub N "<why idle>"`.
