# Planner and workers: several agents, one repo

swim has no lane locks for writers yet (lane claims are a proposed feature),
so the planner's assignment is the only thing that keeps agents apart.
These rules make it hold. They extend the guide's `PLANNER WORKFLOW` and
`WORKER RULES`; read those too.

## The planner

1. **Survey:** run `swim report --earmark` and `swim status`. Free lanes are
   idle, stubbed, or numbers not in use (1–99). A lane that is running,
   pending or queued isn't free, and neither is a lane the repo commits.
2. **Assign one lane number to each worker,** before any worker starts. Put
   it in the worker's prompt, along with:
   - the goal;
   - the exact `# After:` line, if the lane depends on another worker's;
   - its own earmark name (`SWIM_SESSION=worker-orders`);
   - what to report back.
3. **Record the assignment** so other sessions see it:
   `swim note "lanes: 4 orders (worker-orders), 5 billing (worker-billing)"`.
4. **Write the shared parts yourself:** cross-lane ordering, config
   `deps:` lines (handed to the operator), and any shared file. Workers
   send you the lines; they don't edit shared files.
5. **Check every lane together** once the workers are done: `swim lint` (all
   lanes) and `swim status`. Hand the operator one command for the set,
   pinned to the job ids the workers reported.

## A worker

- **Write only your lane.** `swim new N "<goal>"` for your number, then
  `lane.N.*` and nothing else. Never `swim new`, `stub`, `archive` or edit
  another lane, even one that looks abandoned.
- **If your lane isn't free** (`swim new` refuses, or `swim status` shows it
  running, pending or queued), stop and tell the planner. Don't pick
  another number yourself: the planner may have given it to someone else.
- **Put your name in `# Owner:`** in the header, so `swim status` and the
  operator can see whose lane it is.
- **Use names nobody else will.** Tag resources, snapshot labels and temp
  files with `$SWIM_JOB`.
- **Shared files:** a change to a shared file, config or another lane's
  dependency goes back to the planner as exact lines. A change to a
  shared file must leave its other users working.
- **Check, then report:** `swim lint N` until clean. Report back:
  - the lane number;
  - the job id (`# Job:`);
  - the guard flags and what each approves;
  - anything the planner must apply.
- **Never run the lane.** Running and approving are the operator's (SKILL.md
  section 2).

## Two independent sessions in one repo

- **Each session gets its own earmark name** (`SWIM_SESSION`), or their
  `swim report --earmark` calls move each other's place.
- **Before writing,** run `swim status`, then `swim log | tail` for the
  other session's recent notes. Leave your own: `swim note "session-b:
  using lanes 20-24 for the billing work"`.
- **When unsure whether a lane is someone else's,** ask the user. `# Owner:`
  and the notes are conventions, not locks.
