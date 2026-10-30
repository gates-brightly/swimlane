# Concurrent streams, worked through

SKILL.md section 4 gives the rules. This is what it looks like in a
session with three tasks.

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
swim queue add 21                     # → queued #41; job 63bdf4a9
#   task 1: dispatched #41 (21: 63bdf4a9)

# 4. Don't wait: start billing, lane 22.
swim new 22 "billing: raise the queue's retention"
#   ... write lane.22.sh ...

# 5. billing is ready: a stopping point. Dispatch, then look.
swim lint 22
swim queue add 22                     # → queued #42
swim report --earmark --name "$SWIM_SESSION"
swim queue
#   #41 done: failed. #42 running.

# 6. orders finished: pick it up before going on.
swim log 63bdf4a9                     # the gate "role exists in tf state" failed: the import step never ran
#   ... fix lane.21.sh (#41 is done, so the lane is free to edit) ...
swim lint 21
swim queue add 21                     # → queued #43
#   task 1: dispatched #43 (21: 9c2e71d0), fixing the import order

# Back to step 4: start users, lane 23.
swim new 23 "users: drop the legacy index"
#   ... write lane.23.sh: the drop is behind guard USERS_ALLOW_DROP_INDEX ...
swim lint 23
swim queue add 23                     # → queued #44
#   ⚠ swim 23, USERS_ALLOW_DROP_INDEX: drops users_legacy_email_idx in prod.
#     Unapproved, it dry-runs.
#   task 3: waiting on the operator (#44 will dry-run)

# Every task is dispatched or waiting: now block until something new.
swim report --earmark --wait --name "$SWIM_SESSION"
#   #42 passed: check its output (PASS only means exit 0), close billing out.
#   #43 passed: check it, close orders out.
#   #44 skipped: its change was a dry run (USERS_ALLOW_DROP_INDEX not approved). Not validated yet.
```

## When to look

- **Every stopping point:** run `swim report --earmark`. It prints only
  what's new since you last looked, so it stays cheap to run often.
- **Before editing a lane:** run `swim queue`. Is a batch of yours waiting
  on it? Then `swim queue rm <id>` first, or work on something else.
- **Nothing left to do:** run `swim report --earmark --wait`, which blocks
  until there's something new. Not before then.

## Closing a task out

A task is done when its validation passed **and** you've read the output
that shows the outcome: ids, counts, "No changes", the verify stage. A
dry-run SKIP isn't done, and neither is a lane skipped because a
dependency failed. Record the result where the project tracks work,
`swim archive N --as <task>-ok`, and `swim stub N "<why idle>"`.
