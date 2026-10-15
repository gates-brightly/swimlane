# Finally lanes and jobs: run once others end, however they ended

Status: proposed

## Summary

A lane with `# Finally: 1 2` starts once lanes 1 and 2 have **ended in any
state** (passed, failed, skipped or interrupted), where `# After: 1 2` waits
for them to **pass**. It's the run-level counterpart of a lane's `on_exit`
handler: cross-lane cleanup, a run report, a notification. A job marked
`# swim job 1.9 finally` does the same inside a lane of jobs: it runs even
after an earlier job failed.

```bash
#!/usr/bin/env bash
# Round: tear down the orders test stacks, report the run
# Finally: 1 2 3
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 4

stage verify
for n in 1 2 3; do
  v="SWIM_FINALLY_$n"
  msg "swim $n ended ${!v}"
done
run "remove test stacks" ./scripts/teardown.sh orders-test
run "post the run summary" ./scripts/notify.sh "$SWIM_RUN"
summary
```

```
$ swim run 1 2 3
 swim 1  PASS
 swim 2  FAIL exit 1
 swim 3  SKIP  (swim 2 failed)
 swim 4  PASS  finally (1 pass, 2 fail, 3 skip)
run r-0915: 1 passed, 1 failed, 1 skipped · finally: 1 passed · exit 1
```

Always lanes are the run's `before_script`; finally lanes are its
`after_script` (GitLab's `when: always`).

## Motivation

- **`After:` only means "after these pass".** A dependent lane is skipped
  when an upstream fails (`outsideBlocker`, `internal/launcher/deps.go:208`,
  and the in-run skip). Cleanup is needed most exactly when something failed.
- **`on_exit` is per lane.** Outcome handlers (script syntax 3,
  guide.txt *OUTCOME HANDLERS*) cover "clean up after this round". They
  can't wait for other lanes, see their outcomes, or tear down something
  three lanes built together.
- **Lanes of jobs have no finally either.** Jobs run in file order and the
  first failure skips the rest (guide.txt, *job*). A group file
  (`lanes.<name>.sh`) that provisions, tests and tears down can't make its
  teardown job unconditional.
- **Reporting and notification belong in the record.** Today a run report
  or a Slack post is a wrapper around `swim run`, outside the lane log, the
  approvals and the chain. As a finally lane it's an ordinary pinned,
  approved, logged round. The derived-artifact write-up in
  [observe.md](observe.md) uses exactly this.

## Behaviour

### The header

- **`# Finally: <refs>`** takes the same refs as `After:`: lane numbers, job
  ids (a pin to that exact round), `N.K` job paths, `N[cell]`. A lane may
  have both: `# After: 1` (needs 1 to pass) with `# Finally: 2 3` (needs 2
  and 3 to have ended). `After:` decides whether it runs; `Finally:` only
  decides when.
- **"Ended"** is any terminal state of the round in this run: passed,
  failed, skipped (including skipped because its own `After:` failed), or
  interrupted. A lane not in the run counts as ended. A finally lane never
  waits on work that isn't happening.
- **Config `deps:`** gets the same form (`finally: [1, 2]`), for ordering that
  holds for every round.
- **Non-bash lanes** use the same header in their comment syntax, or
  `finally:` in `lane.N.yml`.

### Selection

- **Naming upstream pulls it in.** `swim run 1 2` also runs every finally
  lane whose `Finally:` names a selected lane and holds a pending round, the
  way naming a lane pulls in the Always lanes it names. The plan and the
  live view mark these lanes `[F]`. `--no-finally` leaves them out.
- **`swim all`** runs pending finally lanes like any other pending lane.
- **Running one by name** (`swim run 4`) runs it alone. Its upstream lanes
  aren't in the run, so they count as ended and it starts at once. The plan
  says so (`finally: swim 1 2 3 aren't in this run`).
- **Cycles** are refused like `After:` cycles. A finally lane may name
  another finally lane (cleanup, then report).
- **Always lanes** can't be finally lanes. Every other lane waits for Always
  lanes; a finally lane naming an Always lane is fine.

### Context

- **Per upstream lane:** `SWIM_FINALLY_<N>` is `pass`, `fail`, `skip` or
  `interrupted`, and `SWIM_FINALLY_<N>_JOB` is the job that ran (empty when
  skipped before starting). For a job path, `SWIM_FINALLY_<N>_<K>`.
- **Summary:** `SWIM_FINALLY` is `pass` if every upstream passed, else
  `fail`. `SWIM_RUN` is the run id, so a report can call `swim log
  "$SWIM_RUN"` or `swim timeline "$SWIM_RUN"`.
- **SDK lanes** get the same in the `hello` result (`finally: {1: pass, 2:
  fail}`); no new op is needed.

### Stopping

The same rules as outcome handlers, one level up:

- **First Ctrl-C:** no new ordinary lanes start, but finally lanes whose
  upstream has ended still start. Cleanup is the point. Their steps run even
  though a stop was asked for, and they see `interrupted` for lanes that
  were stopped.
- **Second Ctrl-C:** finally lanes that haven't started are skipped, and the
  running ones are interrupted like any lane. **Third:** kill, as today.
- **Budget:** a run stopping for any reason (Ctrl-C, `swim interrupt`, a
  hub interrupt) gives its finally lanes `finally_timeout` (config, default
  10m) in total. Past that, they're interrupted. Each lane's own `Timeout:`
  still applies.
- **Approvals:** after a Ctrl-C nobody is asked, as with handlers. A
  guarded rollback in a finally lane needs approving up front
  (`FLAG=1`, `queue --approve`, a signed approval). Otherwise it's a dry run.

### Results

- **A finally lane's result never changes an upstream lane's.** The run
  summary reports finally lanes on their own line (`finally: 1 passed`), and
  `run-done` in `.swim.log` adds `finally_passed=` / `finally_failed=`.
- **Exit code:** the run's exit code is the ordinary lanes' result.
  If every ordinary lane passed and a finally lane failed, the run exits 1.
  If an ordinary lane failed and a finally lane failed too, the run exits
  `89`, the code already used for "failed, and the rollback failed".
- **`swim all` next time:** a finally lane that passed counts as passed like
  any lane (`AlreadyPassed`). One whose upstream changed (a new round) is
  pending again, so it runs with them.

### Finally jobs

```bash
# swim job 3.1
# Round: provision the test stack
...
# swim job 3.2
# Round: run the integration suite
...
# swim job 3.9 finally
# Round: tear the test stack down
...
```

- **`# swim job N.K finally`** runs after every earlier sibling job has
  ended, even when one failed and the rest were skipped. Several finally
  jobs run in file order, each after the one before.
- **Nesting:** a finally job covers its siblings and their children
  (`1.5.9 finally` waits on everything under `1.5` before it).
- **Context:** `SWIM_FINALLY_<N>_<K>` per sibling, and `SWIM_FINALLY`, as
  for lanes.
- **The lane's result** counts only the ordinary jobs. A failed finally job
  shows in the lane's `jobs:` line and its END block (`finally: 3.9 FAIL`),
  and the lane exits 89 if an ordinary job also failed.

### Visibility

| Where | Shows |
|---|---|
| `swim plan` | `[F]` on finally lanes and jobs; `finally after: 1 2 3`; `pulled in by: swim 1` |
| live view, watch screen, hub | `waiting (finally) on 2 3`; the upstream outcomes once it starts |
| `swim status` | `finally: 1 pass, 2 fail, 3 skip` under the lane after it ran |
| `swim timeline` | finally lanes after their upstream lanes, with their wait counted as `finally`, not `dependency` |
| `.swim/runs/RUN.yml`, `swim.run/v1` | `finally: true` on the lane; `waiting` events carry `finally: [2, 3]` |
| chain | the round entry records `finally` and the upstream outcomes it saw |

## Design

| Area | Change |
|---|---|
| `internal/lane` | Parse `# Finally:` (all runtimes and `lane.N.yml`) and `# swim job N.K finally`. `Info.Finally []string`, `SubJob.Finally bool`. |
| `internal/launcher/deps.go` | `Dep` gains `Kind` (`after`, `finally`, `setup`). `ResolveDeps` (`:44`) resolves `Finally:` refs; cycle detection covers both kinds. `DepEdges` (`:289`) exposes the kind, so the queue's schedule hold (`swim schedule`) and `swim plan` use it. |
| `internal/launcher` | The wait loop treats a `finally` dep as satisfied by any terminal outcome and never skips for it. `Select` (`:190`) pulls in finally lanes the way `expandAlways` (`always.go:27`) pulls in Always lanes; `--no-finally`. Stop handling: on the first stop, keep starting finally lanes whose deps have ended, under `finally_timeout`. |
| `internal/launcher/launcher.go` | `laneEnv` (`:1293`) adds `SWIM_FINALLY*`. `printSummary` (`:872`) gets the finally line; the exit code rule. `writeRunRecord` (`:964`) marks finally lanes. |
| `internal/cli/lanesock.go` | `hello` adds `finally`. |
| Group runner (`.swim/groups/`) | Run finally jobs after an earlier sibling's failure instead of skipping them; per-sibling context. |
| `internal/status`, `internal/timeline`, `internal/runrec` | `finally` fields; the `finally` wait kind. |
| `internal/history`, `internal/chain` | `run-done` counts; the round entry's `finally` outcomes. |
| `internal/lint` | Warn when a finally lane has a guard but nothing approves it up front in a queued batch. Error on an Always lane with `Finally:`, and on `Finally:` naming itself. |
| `internal/config` | `finally_timeout`; `deps:` gains `finally:`. |
| `internal/help`, README | `Finally:` next to `After:` in the header list; a FINALLY section after OUTCOME HANDLERS; planner workflow step 8 mentions it. |

### Implementation steps

1. **Parse** `Finally:` and `finally` jobs; lint errors for the invalid
   forms. Unit tests for every runtime's comment syntax.
2. **Dependency kinds.** Add `Dep.Kind`; resolve `Finally:` refs; cycles
   across both kinds. Keep `After:` behaviour byte for byte (the existing
   deps tests must pass untouched).
3. **The wait.** A finally dep is satisfied by any terminal state; a lane
   whose only unmet deps are finally deps is never skipped for them.
4. **Context env** and `hello`.
5. **Selection:** pulling in by upstream, `--no-finally`, and `swim plan`
   output.
6. **Stopping:** first-stop behaviour, `finally_timeout`, second-stop skip.
7. **Results:** the summary line, exit codes (1 and 89), `run-done`, status,
   timeline, chain.
8. **Finally jobs** in the group runner.
9. **Docs.**

## Open questions

- **Name.** `Finally:` (try/finally) or `After-Any:`? `Finally:` says the
  intent (cleanup that always runs), and pairs with `on_exit` without
  colliding with it. `After-Any:` reads as a variant of `After:`, which it
  isn't, because it never skips.
- **Pull-in by default?** This spec pulls finally lanes in when an upstream
  is named, because cleanup belongs with the work. That surprises someone
  running `swim run 1` to retry one lane. Should it be the default only in
  `swim all` and queued batches?
- **Partial upstream.** If `swim run 1` pulls in lane 4 (`Finally: 1 2 3`)
  but 2 and 3 aren't selected, should lane 4 run (they count as ended), or
  wait for a run that includes all three? This spec says run. A report lane
  then reports a partial run, and says so through `SWIM_FINALLY_<N>`
  being empty for lanes not in the run.
- **Queue batches.** Should `swim queue add 1 2` pull finally lanes into
  the batch, pinned with it? It should, for the same reason, but it means the
  batch holds more lanes than the operator typed. The reply would list them.

## Testing

- **Unit, deps:** `Finally: 1` waits for 1 and runs after 1 passes, fails,
  is skipped, or is interrupted; `After: 1` + `Finally: 2` is skipped when 1
  fails, whatever 2 did; cycles across kinds are refused; existing
  `After:` behaviour is unchanged.
- **e2e, the run:** lanes 1 (pass), 2 (fail), 3 (`After: 2`, skipped) and 4
  (`Finally: 1 2 3`). Lane 4 runs last, sees `pass fail skip`, and the run
  exits 1. Then make lane 4 fail too: exit 89.
- **e2e, Ctrl-C:** stop the run while lane 2 is running. Lane 4 still starts
  after 2 ends, sees `interrupted`, and is bound by `finally_timeout`. A
  second Ctrl-C before it starts skips it.
- **e2e, selection:** `swim run 1` pulls in lane 4; `--no-finally` doesn't;
  `swim run 4` alone starts at once.
- **e2e, finally job:** jobs 3.1 (pass), 3.2 (fail) and 3.9 finally. 3.9
  runs, the lane is FAIL, and its END block shows `finally: 3.9 PASS`.
- **Approvals:** a guarded step in a finally lane, after a Ctrl-C, with no
  up-front approval, is a dry run, and with `queue --approve` it runs.
- **Chain:** `swim verify` passes; the round entry lists the outcomes the
  finally lane saw.

## Out of scope

- Run-level hooks outside lanes (a command in config run after every
  `swim run`). Everything that runs is a lane, pinned and approved.
- Finally lanes across runs or batches: a finally lane only waits on lanes
  in its own run.
- Notification integrations. A finally lane runs whatever the round says;
  swim ships no Slack or email client (a later module may).
