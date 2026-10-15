# swim pause: hold lanes at their next step, then resume or replace them

Status: in progress (phase 1 shipped: the CLI, the hold in bash and SDK
lanes and before guards, the deadline, waiting lanes, Ctrl-C, replace,
`--all` and workers, status, the live view's paused row, docs)

### Phase 2 (not built)

- The live view's and watch screen's `p`/`P` keys and the watch banner.
- `pause`/`unpause` as hub commands; paused lanes on the hub's pages.
- `swim timeline`'s `paused` segment and the run record's pause spans.
- The chain: pause records and `replaced_by` on round entries.
- `swim doctor`'s `pause_warn` (lanes paused over 4h; a repo-wide pause
  left on).
- `swim plan` marking lanes it would wait on as paused.
- The SDK's own deadline (`hello`'s) doesn't move out with paused time;
  the bash library's and `swim step`'s do.

### Where phase 1 differs from below

- **No `paused` state.** A paused lane stays `running` (or `waiting`) in
  `status.yml`, with `paused_*` fields: its process really is alive, so
  every check that treats a lane as running (refusals, reaping, busy) stays
  right with no change. `swim status` shows it as `paused`.
- **No log syntax bump.** `PAUSE` and `RESUME` lines are skipped by a swim
  from before them (its parser ignores result kinds it doesn't know), so
  repos don't need `swim lock --upgrade` for them.

## Summary

`swim pause N` lets lane N's current step finish, then **holds** the lane
before its next step starts. The round stays alive: its process, its staged
script, its approvals and its place in the run. Two ways out:

- **`swim unpause N`** (or `swim resume N`, the same command) resumes it
  at the step it was about to start.
- **Running a different job on the lane** (`swim run N` once a new round is
  written, or `swim run <job>`) ends the paused round as interrupted, with
  its outcome handlers run, then starts the new one.

Other lanes aren't affected. While lane 3 is paused, `swim run 5` in another
terminal works as it does today.

```
$ swim pause 3
swim 3: pausing after "tf plan orders" (running, 1m12s); no further step will start until swim unpause 3

$ swim status 3
 swim 3  paused 4m           cut over orders to terraform
          job: 3f2a9c1e  paused by zach after "tf plan orders"; next: "tf apply orders"
          resume: swim unpause 3 · end it: swim interrupt 3

$ swim unpause 3
swim 3: resumed (paused 6m10s); next step "tf apply orders"
```

`swim pause --all` holds every running lane in the repo, keeps lanes
waiting in a run from starting, and stops workers claiming new batches,
so the operator can step in without killing anything.

## Motivation

- **Stopping today means ending.** `swim interrupt N`
  (`internal/cli/interrupt.go`) and the first Ctrl-C stop a lane at its
  next step boundary, and the round ends interrupted. To carry on, the
  round runs again from the top. Rounds are rerunnable, but a long one
  repeats its snapshot and checks, and re-asks its guards.
- **Operators need a "wait".** "Hold on, someone's mid-deploy on that
  stack." "Let me look at the plan before it applies." "I need the VPN for
  something else for ten minutes." There's no way to say "not yet" to a
  round that's already running, short of killing it.
- **Urgent work shouldn't cost the current work.** An operator who needs to
  run a different job now should be able to park the current one cleanly,
  either to resume after or to drop, and know which steps did and didn't run.
- **The boundary already exists.** Every step goes through `swim step`
  (`internal/cli/commands.go:736`, with its `Stopped` check) or, for SDK
  lanes, the lane socket's `begin_step` and `run` ops
  (`internal/cli/lanesock.go:335`). Pause waits at the same place stop
  checks. No step is ever cut in half.

## Behaviour

### Pausing

```
swim pause N|JOB ... [--wait] [--note TEXT]
swim pause --all [--wait] [--note TEXT]
```

- **When it takes effect.** The current step finishes normally: its
  output, its result line, its retries. The lane then blocks before
  starting the next step. `swim pause` returns at once, saying which step
  it will stop after. `--wait` blocks until the lane is actually holding.
- **Where it can hold:** before any `run`, `gate`, `snapshot`, `evidence`,
  `guard`, `confirm` or in-process step (`begin_step`), between the jobs of
  a lane of jobs, and between matrix cells. A cell process holds the same
  way, so `swim pause 3` holds every running cell of lane 3.
- **Lanes not started yet.** A lane waiting in a run (on a dependency, a
  slot, a lock or the queue) is marked paused and doesn't start until
  unpaused. Lanes that depend on a paused lane keep waiting, as they would
  for a slow one.
- **Not handlers.** Outcome handlers (`on_failure`, `on_exit`) and
  [finally lanes](finally.md) run to completion once the round has ended.
  A pause asked for then applies to nothing and says so. Rolling back halfway
  is worse than either.
- **Not a lane that isn't running.** `swim 3 isn't running` (exit 1), as for
  `swim interrupt`. To keep a lane from starting at all, don't queue it.
- **`--all`** pauses every running lane of every swim run in the repo, plus
  their waiting lanes, and sets a repo-wide pause that queue workers
  (`swim watch`, headless included) honour: they claim no new batches until
  `swim unpause --all`. A scheduled batch that comes due meanwhile waits.
  It isn't missed, since schedules have no closing time.

### While paused

| | |
|---|---|
| The round's process | Alive, holding. It still counts as running, so `swim new`, `stub` and `archive` refuse the lane as they do now. |
| Its staged script | Unchanged. Edits on disk don't reach it, as with any running lane. |
| Its slot (`max_parallel`) | **Held.** The run it belongs to doesn't start another lane in its place, so on resume the run is never over its limit. |
| Its locks (`# Locks:`) | **Held.** The lock protects a resource the round may be halfway through changing, so releasing it isn't safe. `swim status` shows `paused, holding lock orders-table`, and a round waiting for the lock says it waits on a paused lane. |
| Its `Timeout:` | **Not counting.** Paused time is added to the round's deadline, so a pause doesn't push a round into its time limit on resume. Step limits never count, since no step runs. `Timeout:` bounds the round's working time, not wall-clock time. |
| Approvals | Those already used stay used. Guards after the pause ask, or approve, as they would have. `Observe:` probes are read again before a guarded step (as today), so a world that changed during the pause ends the round as drift, not as a stale approval. |
| Credentials | Whatever the round holds (an AWS session, a token) may expire during a long pause. That's the round's business; swim says how long it was paused when it resumes. |
| The operator's terminal | Free. The `swim run` process keeps waiting, its live view shows `paused`, and the operator can run anything else from another terminal or from the watch screen. |

### Resuming

```
swim unpause N|JOB ... | --all
swim resume  N|JOB ... | --all      the same command
```

- `unpause` and `resume` are two names for one command: same flags, same
  behaviour, same records. `.swim.log` and the chain call it `unpause`
  whichever name was typed, so the record doesn't depend on wording.
- The lane starts the step it was about to start. Its log reads
  `PAUSE  by zach after "tf plan orders"` then `RESUME  by zach (paused
  6m10s)`, two result-kind lines that count toward nothing, like `MSG`.
- `--all` lifts every lane's pause and the repo-wide one.
- Unpausing a lane that isn't paused is a no-op and says so (exit 0).

### Running a different job instead

The second way out. On a paused lane:

```
$ swim run 3
swim 3 is paused in run r-0915 (job 3f2a9c1e, after "tf plan orders").
  lane.3.sh now holds job 7b4d4e2a ("drop the orders index").
End the paused round as interrupted and run job 7b4d4e2a? [y/N] y
swim 3: interrupted job 3f2a9c1e (on_failure ran: PASS); starting 7b4d4e2a
```

- **What it does:** a stop request on the paused round, the same one
  `swim interrupt` sends. The round wakes, records `STOP  interrupted by
  operator (paused, replaced by job 7b4d4e2a)`, runs its outcome handlers
  (`SWIM_OUTCOME=interrupted`), and ends (exit 130). Once it has exited, the
  new round starts in the new run.
- **Confirmation:** at a terminal it asks, defaulting to no. Without a
  terminal it refuses unless `--replace` is given. A queued batch holding the
  lane counts as no terminal, so a worker never replaces a paused round on
  its own. The batch waits, as it would for any running lane.
- **The same job:** if `lane.3.sh` still holds the paused job, `swim run 3`
  says `swim 3 is paused on this job: swim unpause 3 resumes it, swim run
  3 --replace restarts it from the top`.
- **Another lane:** `swim run 5` while 3 is paused just runs, unless it
  needs a lock 3 holds or names 3 in `After:`. Then it waits, and the live
  view says on what.
- **`swim interrupt N`** on a paused lane ends it as above, without starting
  anything.

### Stopping a run with paused lanes

- **First Ctrl-C** in the `swim run` that owns them: paused lanes are
  already at a boundary, so they end interrupted at once, with their
  handlers run. That's the same result as a lane stopped mid-run.
- `swim run` exiting for any other reason (the terminal closed, a crash)
  leaves the lane as today: its process dies or is killed, and the next run
  or watch records it interrupted. The pause request is cleared at the next
  `lane_init`, like a stale stop request (`hooks.go:160`).

### Visibility

| Where | Shows |
|---|---|
| `swim status`, `status.yml` | state `paused`, `paused_at`, `paused_by`, `paused_after` (the step), `next_step`, `pause_note`; held locks |
| live view (`swim run`) | `paused` on the lane's row; `p` pauses or unpauses the selected lane, `P` all its lanes |
| watch screen | `paused` rows; `p` pauses or unpauses the selected lane; a banner while the repo-wide pause is on (`paused: workers claim nothing · swim unpause --all`) |
| hub | paused lanes and the repo-wide pause; `pause`/`unpause` as headless commands, if the repo's `headless:` allows them (relayed and recorded like `interrupt`) |
| `swim plan` | `paused` on lanes it would wait on |
| `swim report` | the `pause`/`unpause` lines from `.swim.log` |
| `.swim.log` | `pause` and `unpause` (who, lane, job, run, after which step, note) and `pause-all` / `unpause-all` |
| chain | the round entry records each pause's start, end and who, and total paused time. A replaced round records `replaced_by: <job>`. |
| `swim timeline` | a `paused` segment in the lane's bar, separate from step time and from dependency, slot and lock waits |

## Design

| Area | Change |
|---|---|
| `internal/lane` | `PausePath(root, n)`, `RequestPause`, `PauseRequested`, `ClearPause` beside the stop request (`lane.go:967`); `.swim/pause` for the repo-wide pause. A pause file records run, who, when and note, so a stale one (another run) is ignored like a stale stop. |
| `internal/cli/pause.go` (new) | `cmdPause`, `cmdUnpause` (registered as both `unpause` and `resume`): resolve lanes, check they're running, write or clear the requests, `--wait`, history lines. Unpause wakes the lane (below). |
| `internal/cli/commands.go` (`swim step`, `:736`) | Before running the step: if paused, record `PAUSE`, update status, and block until the pause is cleared or a stop is requested. Wake on `SIGUSR1` from `swim unpause`, polling as a fallback. On wake, add the paused time to the round's deadline, then run the step (or end, if stopped). |
| `internal/cli/lanesock.go` | The same hold before `begin_step`, `run`, `gate`, `snapshot`, `evidence`, `guard` and `confirm`. The SDK call simply takes longer to answer; SDKs need no change. |
| Deadline | `SWIM_DEADLINE` is fixed in the environment, so the paused total lives in the lane's status entry (`paused_s`). Every reader of the deadline (`commands.go:729`, `approve.go:343`, `lanesock.go:240`) adds it. |
| Group runner, matrix | Hold between jobs and between cells, at the same check. |
| `internal/launcher` | Paused waiting lanes don't start. Paused lanes keep their slot and locks. Ctrl-C ends paused lanes. The live view's `p`/`P`. The replace path: `swim run` on a paused lane asks, or takes `--replace`, sends the stop, waits for the lane to exit, then runs. |
| `internal/queue`, `cmdWatch` | Workers honour the repo-wide pause (no claims); the watch screen's `p` and banner. |
| `internal/status`, `internal/runrec`, `internal/timeline` | `paused` state and fields; pause segments in the run record; the `paused` segment kind. |
| `internal/logparse` | `PAUSE`/`RESUME` result kinds that count toward nothing (like `MSG`). This changes the log's syntax, so it bumps the log syntax version and `.swim.lock`, and older swims must read the new lines as unknown. |
| `internal/history`, `internal/chain` | `pause`, `unpause`, `pause-all`, `unpause-all`; pause records and `replaced_by` on round entries. |
| `internal/cli/hubcmds.go`, `headless:` | `pause`/`unpause` as relayed commands, off unless allowed. |
| `internal/doctor` | Lanes paused longer than `pause_warn` (config, default 4h); a repo-wide pause left on. |
| `internal/help`, README, `swimlane.md` | `pause`/`unpause` next to `interrupt`, with `resume` named as the same command; INTERRUPTING gets a PAUSING paragraph; the brief's Running requirements gain one line. |

### Implementation steps

1. **Requests:** pause files beside stop files, with the stale-run rule;
   `swim pause` / `swim unpause` for running lanes, `--wait`, history lines.
2. **The hold in `swim step`:** block before the step, wake on signal or
   poll, stop while paused ends the round, deadline extension. Unit test
   the wait loop with a fake clock.
3. **The hold in the lane socket** for SDK lanes. Run the conformance tool
   (`sdk/go/conformance`) against a paused Go lane.
4. **Jobs and cells:** hold between them.
5. **Log lines** `PAUSE`/`RESUME`, the log syntax bump, and the parser.
6. **Run integration:** waiting lanes don't start, Ctrl-C ends paused lanes,
   live view keys.
7. **Replace:** `swim run` on a paused lane, its prompt and `--replace`, the
   same-job case.
8. **Repo-wide pause:** `--all`, workers honouring it, the watch banner and
   `p`.
9. **Visibility:** status, timeline, plan, chain, doctor, hub commands.
10. **Docs.**

## Decisions

Settled 2026-10-15.

- **A paused lane holds its slot.** The run doesn't start another lane in
  its place, so it's never over `max_parallel` on resume. Locks are held
  too (see While paused).
- **`Timeout:` doesn't count while paused.** Paused time is added to the
  round's deadline, so `Timeout:` bounds working time. An operator who needs
  a wall-clock bound ("never run past 23:00") uses a schedule or ends the
  round themselves.
- **A pause doesn't expire.** No `--for D`, no limit, nothing ends or
  resumes a paused lane on its own. `swim doctor` still warns about lanes
  paused longer than `pause_warn` (default 4h), because a long pause holds
  its locks. The warning is all it does.
- **No pausing inside a step.** No SIGSTOP. The pause takes effect at the
  next step boundary, and `swim pause` says which step is running and for
  how long, so the operator knows the wait.
- **`unpause` and `resume` are the same command.** Both names are accepted,
  with identical flags and behaviour. Help lists `unpause` and names
  `resume` beside it. Records always say `unpause`.

## Testing

- **Unit, the hold:** pause requested during a step → the step finishes,
  the next doesn't start, status is `paused` with the right step names;
  unpause → the next step runs; stop while paused → the round ends
  interrupted with handlers run; a stale pause from another run is ignored.
- **Unit, deadline:** a round with `Timeout: 2m` paused for 5m (fake clock)
  resumes with its remaining time intact.
- **Unit, no expiry:** a lane paused for 25h (fake clock) is still paused,
  with only the doctor warning.
- **e2e, bash lane:** a three-step lane; `swim pause 1 --wait` during step
  1; step 2 hasn't started (no block in the log) and the log has `PAUSE`;
  `swim unpause 1`; the round passes with all three steps and `RESUME`
  (and again with `swim resume 1`: same log lines, `.swim.log` says
  `unpause`);
  `swim verify` passes.
- **e2e, SDK lane:** the same with a Go lane (begin_step and run held).
- **e2e, jobs and cells:** a lane of jobs paused between 1.1 and 1.2; a
  matrix lane with every cell held.
- **e2e, replace:** pause lane 1 on job A, write job B, `swim run 1
  --replace` (no terminal): A ends interrupted with `on_failure` run, B runs
  and passes; without `--replace` and no terminal, refused; on the same job,
  the unpause/restart message.
- **e2e, other lanes:** with 1 paused, `swim run 2` (independent) runs and
  passes; a lane sharing 1's lock waits and says it waits on a paused lane.
- **e2e, `--all` and the worker:** with `swim watch` running a batch,
  `swim pause --all` holds it, a newly queued batch isn't claimed, and
  `swim unpause --all` resumes both.
- **e2e, Ctrl-C:** a paused lane ends interrupted on the first Ctrl-C of its
  run.
- **PTY:** the live view's and watch screen's `p` toggles pause and the row
  shows it.

## Out of scope

- Pausing in the middle of a step (SIGSTOP).
- Surviving the `swim run` process: a pause holds a live round. It isn't a
  checkpoint a later run resumes from after a crash or reboot.
- Pausing lanes on other machines through anything but the hub's relayed
  commands.
