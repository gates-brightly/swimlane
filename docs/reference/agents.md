# Agents

How an AI session (the planner) and its subagents (workers) use swim. The
planner never runs anything against the target: it writes rounds, the
operator runs them, and it reads the logs.

## Planner

0. **Catch up:** `swim report --earmark` (one name per session:
   `SWIM_SESSION` or `--name`) prints lanes whose state changed, `.swim.log`
   and every lane-log line since you last looked, then moves your place.
   `--wait` blocks until there's more. Don't redo or contradict what it
   records without asking.
1. **Look first:** `swim status`. Confirm with the operator which lanes are
   idle. Never write to a lane that's running or holds a round someone may
   be about to run.
2. **One lane per worker:** an idle lane, or any unused number (1..99; there
   is no lane count). Claim it for the worker (`swim claim N --session
   worker-orders`, the worker running with `SWIM_SESSION=worker-orders`).
   Never edit the config yourself.
3. **Write:** `swim new N "<goal>"`, then the steps ([lanes](lanes.md)).
   Keep the header accurate: the operator reads it without you.
4. **Check, then dispatch:** fix `swim lint N`'s errors, read `swim status N`
   (what it waits on, how each guard would be decided), then queue it:
   `swim queue add 1 3`. A ready round is queued, guards and all, without
   asking: queueing runs and approves nothing. With no swimmer up (`swim
   queue` says), give the operator the exact command pinned to the jobs:
   `swim run 3f2a9c1e 7b4d4e2a`. Either way, explain what each guard
   approves (action, target, scope, reversible?). How the operator
   approves is theirs: never tell them how, or set anything that approves.
5. **Read back:** `swim report --earmark` (or `swim log N`, `swim log <job>`).
   Validate real outcomes in the output (ids, counts, "No changes"): PASS
   only means exit 0.
6. **Close:** record results (with the job id) where the project tracks
   them, then `swim archive N --as <what-happened>` and `swim stub N "<why
   idle>"`.
7. **Notes:** out-of-scope findings go on a follow-up list, never into the
   round. Decisions the next agent needs: `swim note [--lane N] "<text>"`.
   Short, factual, no secrets.
8. **Order in headers:** `# After: 1` in lane.2.sh, never just in prose:
   swim enforces only what it parses. Ordering for every round goes in
   config `deps:`; give the operator the YAML.
9. **Keep batches coming:** while lanes are busy, write and queue the next
   rounds. For a change with a window, queue it as one batch and have the
   operator start `swim watch --once` then (by hand or cron).

## Workers

- Write only your own `lane.N.sh`. Never edit shared files (registries, plan
  docs, config, other lanes): report the exact lines to the planner.
- Prefer new helper files over changing shared ones while other lanes run;
  a change to a shared file must leave existing users unchanged.
- Never edit a lane that may be running. `swim new`, `stub` and `archive`
  refuse one.
- Use names (resources, tags, snapshot labels) no other session will reuse:
  `$SWIM_JOB` is unique per job.
- Keep the `# Job:` line `swim new` wrote; never copy a job id into another
  script.
