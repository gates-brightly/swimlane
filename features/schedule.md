# swim schedule: queue a batch that starts no earlier than a time

Status: proposed

## Summary

`swim schedule` is `swim queue add` with a start time. The batch is pinned
and approved now, exactly as a queued batch is. A worker (`swim watch`) doesn't
claim it before its start time; from then on it is an ordinary waiting
batch, run as soon as its lanes are free. Anything
that depends on the scheduled lanes waits with them, with nothing extra to
declare.

```
$ swim schedule 1 13:30:00
Lane 1 scheduled for 13:30:00(CDT) 18:30:00(UTC)
  q-20261015-0915-ab12 · swim schedule rm q-...ab12 cancels

$ swim schedule 1 1:30pm          # same
$ swim schedule 1 1330            # same
$ swim schedule 1 3 0600 --approve FIN_ALLOW_DELETE_ZG_ITEMS
Lanes 1, 3 scheduled for 06:00:00(CDT) 11:00:00(UTC) tomorrow (2026-10-16)
```

Phase two adds a **recurring** batch: the same pinned rounds, started every
day at a wall-clock time until it expires.

```
$ swim schedule 4 06:00 --daily --until 2026-11-14
Lane 4 scheduled daily at 06:00:00(CDT) 11:00:00(UTC), first 2026-10-16, until 2026-11-14
```

## Motivation

- **Changes have windows.** Cutovers, deletes and migrations often have to
  start inside a maintenance window or after business hours. Today the
  operator either stays up to type `swim run`, or queues the batch and hopes
  `swim watch` reaches it at the right time. The queue starts a batch as soon
  as its lanes are free (`ClaimNext`, `internal/queue/queue.go:396`), which
  is usually too early.
- **The planner can finish early and hand off.** Pillar 4 ("neither side
  blocks the other"). The planner writes the round at 15:00, the operator
  approves it at 16:00 with `--approve`, and a worker starts it at 22:00 with
  no one at a terminal. Approval and execution stay as separate as they
  already are for a queued batch.
- **Downstream ordering should come for free.** A round with `# After: 1`
  must not run before lane 1's scheduled round has passed. With the queue as
  it is, a later batch holding lane 2 would be claimed first (oldest
  waiting entry first, skipping the not-yet-due one) and lane 2 would be
  skipped as "swim 1's pending round has not passed" (`outsideBlocker`,
  `internal/launcher/deps.go:207`). The schedule has to hold dependents back,
  not just its own entry.
- **Standing daily jobs** (exports, reconciliations, health sweeps) run
  through cron and a wrapper script today, outside swim's approvals, logs and
  chain. A recurring batch brings them inside.

## Behaviour: one-shot schedules

### The command

```
swim schedule LANES... TIME [--tz ZONE] [--approve FLAG]... [--note TEXT] [--parallel N] [--key PATH]
swim schedule list [--all] [--yaml]
swim schedule rm ID
```

- **Lanes are everything but the last argument.** As with `swim queue add`,
  lanes are numbers, job ids, or group names (`laneRef`), and every one is
  pinned to its current job and execution manifest (`pinLane`,
  `internal/cli/queue.go:280`). The last argument is always the time, so
  `swim schedule 1 1330` can't be read as lanes 1 and 1330.
- **Everything `queue add` takes, it takes too**: `--approve`, `--note`,
  `--parallel`, `--key` (signing). The batch is built by the same
  `addBatch` (`internal/cli/queue.go:210`), so a scheduled batch is exactly a
  queued batch with a start time.
- **`-y` is refused** with a pointer to `--approve FLAG`. Approving every
  guard for a run hours away, with no one watching, is the case `-y` is
  least suited to. (Open question: allow it with `--yes-really`?)
- **`swim queue add 1 --at 13:30`** is the same command, for people who
  start from the queue. `swim schedule` is the name in docs and help.

### Times

| Input | Means |
|---|---|
| `13:30`, `13:30:00`, `1330`, `133000` | 24-hour wall clock |
| `930`, `9:30` | 09:30 (no am/pm means 24-hour; the echo shows which it took) |
| `1:30pm`, `1:30 pm`, `1pm`, `12am` | 12-hour |
| `2026-10-20 13:30`, `2026-10-20T13:30` | a date and a time |
| `tomorrow 06:00` | tomorrow's date and a time |
| `+2h`, `+90m` | relative to now |

- **The zone** is `--tz` (an IANA name such as `America/Chicago`), else
  `schedule.tz` in config, else the machine's local zone (`TZ`, then
  `/etc/localtime`). The stored time is UTC; the zone name is kept for
  display and for recurring schedules.
- **A bare time that has already passed today means tomorrow.** The reply
  says so (`tomorrow (2026-10-16)`). A dated time in the past is refused.
- **Invalid input is refused, never guessed.** `25:00`, `13:61`, `1:30xm`,
  and two-digit `13` (hour or lane?) are refused with the accepted forms.
- **DST.** A wall-clock time that doesn't exist that day (spring forward) is
  refused for a one-shot, with the nearest valid times named. One that occurs
  twice (fall back) takes the first, and the reply shows the UTC time.

### The reply

Exactly the requested line, then one detail line:

```
Lane 1 scheduled for 13:30:00(CDT) 18:30:00(UTC)
  q-20261015-0915-ab12 · swim schedule rm q-...ab12 cancels
```

- `Lanes 1, 3` for several lanes. ` tomorrow (YYYY-MM-DD)` or ` on
  YYYY-MM-DD` is added when the start time isn't today in the given zone. When
  the UTC date differs from the local date, the UTC part carries its date.
- **Warnings go after it**, never instead of it:
  - **No worker running.** No `swim watch` holds the worker lock
    (`LockWorker`, `internal/queue/queue.go:504`) and none is installed
    headless: `no swim watch is running here; nothing will start this at
    13:30 (swim watch --headless --detach)`.
  - **Something ahead of it.** An unscheduled batch that waits on the same
    lanes would run first.
  - **A pending dependency.** A lane the batch depends on holds a pending
    round outside the batch, so the batch would be skipped if that lane
    hasn't passed by then.
- `--yaml` prints `swim.schedule/v1` (id, lanes, `not_before` in UTC, zone,
  warnings).

### The start time

- **`not_before` is the only gate.** No worker claims the batch before it.
  From then on it is an ordinary waiting batch, with no closing time.
- **Late is late, not cancelled.** If lanes are busy, other batches are
  ahead of it, or no worker runs at 13:30, the batch starts when a worker
  next can, as any queued batch does. A deadline would interact badly with
  the rest of the queue (a batch expiring while it waits behind others, and
  its held dependents with it), so there is none. To stop a batch that
  hasn't started, `swim schedule rm`.
- **Late starts are visible.** `swim status`, the watch screen and the
  `schedule-start` line say `started 2h10m after its scheduled time`, and
  the chain records both times.
- **Order among due batches.** Once due, a scheduled batch takes its place
  in the queue by its start time, so a batch scheduled for 13:30 goes ahead
  of one queued unscheduled at 13:45, and behind one queued at 13:15.

### Downstream gating

The rule that makes dependents wait without extra declarations:

> A waiting batch is **held** while any lane it holds depends (by `After:`,
> config `deps:`, or a job pin, at any depth) on a lane held by an
> earlier-scheduled batch that hasn't run yet.

- **Same batch.** `swim schedule 1 2 13:30` with `lane.2.sh` `# After: 1`:
  the batch starts at 13:30, and the launcher orders 1 before 2 as it does
  today. This needs nothing new.
- **A later batch.** `swim queue add 2` after `swim schedule 1 13:30`: batch
  2 is held until batch 1 has run. Then lane 2's normal `After: 1` check
  applies: it runs if lane 1 passed and is skipped if lane 1 failed. Held
  batches don't block unrelated batches behind them.
- **Removed upstream.** `swim schedule rm` lifts the hold and the
  dependent batch goes through the normal check. Since lane 1's pending
  round never ran, lane 2 is skipped with the usual reason, plus `(swim 1's
  scheduled batch q-... was removed)`. The `rm` reply lists the batches it
  releases, so the operator can remove those too.
- **Run directly.** `swim run 2` while lane 1 is scheduled behaves as today
  (lane 2 skipped: swim 1's pending round has not passed), and the reason
  adds `swim 1 is scheduled for 13:30:00(CDT)`.
- **Running a scheduled lane early.** `swim run 1` while lane 1 is in a
  scheduled batch is refused: `swim 1 is scheduled for 13:30:00(CDT) in
  q-...; swim schedule rm q-... first, or swim queue run q-... to start it
  now`. Otherwise the scheduled batch would run the round a second time.

### Visibility

| Where | Shows |
|---|---|
| `swim status` | lane state `scheduled` with `13:30:00(CDT) 18:30:00(UTC) · in 2h10m`, and `held by schedule q-...` on a held dependent. `status.yml` adds `scheduled_for` (UTC) and `schedule` (the entry id). |
| `swim queue` / `swim schedule list` | scheduled batches by start time. `list` hides consumed entries unless `--all`. |
| `swim plan` | a scheduled lane as `action: scheduled`, its dependents as `held` |
| watch screen, hub | a `scheduled` row with a countdown; the hub can't change the time (it relays only the commands `headless:` allows) |
| `swim report` (see [report.md](report.md)) | schedule events appear as `.swim.log` lines |

### Record

- **`.swim.log`** gets `schedule` (id, lanes, not_before UTC, zone,
  approvals), `schedule-start` (the claim, with how late it was), and
  `schedule-rm`. Consuming the batch writes the usual `queue-done`.
- **Signed.** The start time is part of what's authorized. When a batch has
  `not_before`, the queue signature statement (`Entry.Statement`,
  `internal/queue/queue.go:526`) adds a `not-before:` line.
  Unscheduled entries keep today's statement byte for byte. A worker refuses
  a batch whose signed start time doesn't match its file, so editing the YAML to
  move the time voids it.
- **Chain.** The round entry for a scheduled run records `scheduled_for`,
  so `swim verify` can show that a run didn't start early.

## Behaviour: recurring (phase two)

### The command

```
swim schedule LANES... TIME --daily [--until DATE | --for D] [--tz ZONE] [--approve FLAG]...
swim schedule pause ID | resume ID | rm ID
```

- **A recurring schedule is a template, not a queue entry.** It lives in
  `.swim/queue/recurring/<id>.yml` with the pinned lanes, the approvals, the
  wall-clock time, the zone and the expiry. Workers never claim templates.
- **Each occurrence is an ordinary scheduled batch.** When an occurrence
  is due, a worker creates a one-shot entry from the template (`from: r-...`,
  `occurrence: 2026-10-16`) and claims it. It's created with create-exclusive
  under a name made from the template id and the occurrence date, so two
  workers sharing a queue make it once. Everything above (the start time,
  downstream holds, the record) applies to each occurrence unchanged.
- **Wall clock, in its zone.** `06:00 America/Chicago` stays 06:00 local
  across DST. An occurrence whose time doesn't exist that day runs at the
  first valid minute after it, and the record says so.

### Rules that keep it safe

- **It must expire.** `--until DATE` or `--for D`, 30 days by default,
  printed in the reply, and never longer than `standing.max` in
  `lookup.yml`. That's the rule standing approvals follow (`swimlane.md`,
  *Approval lifecycle*), for the same reason: the approvals it carries are
  approvals for every day, so they get the same limit. Renewing is
  scheduling it again.
- **Pinned means pinned.** Each occurrence checks the template's manifest.
  If a script, include, sidecar, runner or policy changed, that occurrence
  is skipped and the template is **paused**, with the reason
  (`schedule-paused r-...: lane.4.sh changed`). It never runs a changed
  script on yesterday's approval. Re-scheduling re-pins.
- **Approvals.** `--approve FLAG` only, signed with `--key` when the repo's
  `lookup.yml` requires signed approvals for those guards. `-y` is refused.
  The approvals bind the template's manifest, so they go stale on the same
  edits that pause it.
- **No catch-up, no stacking.** A worker makes at most one occurrence per
  template at a time. After downtime it makes only the latest one that's
  due, which then runs late like any scheduled batch. Earlier days are
  recorded as `skipped: superseded` and never run. An occurrence that
  comes due while the previous one is still running is recorded as
  `skipped: overlap` and isn't queued.
- **The planner workflow.** A recurring lane's script must not be archived
  or stubbed while scheduled, since a stub changes the script and pauses it.
  `swim stub`/`swim new` on such a lane are refused, like an Always lane
  (`refuseAlways`), unless `--force`, which pauses the schedule and says
  so. Archiving the log is fine. Recurring work fits group files
  (`lanes.<name>.sh`) best: committed, named and standing.
- **Downstream.** Put dependents in the same recurring batch; they're
  ordered within each occurrence for free. A dependent in a separate
  recurring schedule at a later time is held by the upstream's same-day
  occurrence. A pass from yesterday's occurrence never satisfies today's
  dependent.

### Changes to the brief

`swimlane.md` lists **"Scheduling across rounds… there is no cron"** under
*Out of scope*. One-shot schedules don't contradict it: they gate one round
once. Recurring schedules do. Shipping phase two means moving that line to
*Resolved since*, worded as: "Recurring schedules: daily, pinned to exact
scripts, expiring like standing approvals, run by a worker you start. swim
still runs no daemon of its own."

## Design

| Area | Change |
|---|---|
| `internal/schedule` (new) | `ParseTime(s, now, zone) (time.Time, error)` for every form in the table, with DST handling; `Next(wall, zone, after)` for recurring; `FormatReply(t, zone, now)` for the reply line. Pure functions, no I/O. |
| `internal/queue` | `Entry` gains `NotBefore`, `Zone`, `From`, `Occurrence`. `ClaimNext` takes the time, skips entries not yet due, and orders due ones by start time (an unscheduled entry's is its `created`). `Statement` adds `not-before:` only when set. New `Held(root, entries, deps)` applies the downstream rule. |
| `internal/queue` (phase two) | `Recurring` type and its directory; `Materialize(root, r, occurrence)` with create-exclusive; pause, resume and expiry. |
| `internal/launcher` | Expose the dependency graph (`deps.go`) so `queue.Held` and `swim plan` use the same edges the launcher enforces. `outsideBlocker` adds the schedule to its reason. `swim run` refuses a lane held by a scheduled batch. |
| `internal/cli/schedule.go` (new) | `cmdSchedule`: split lanes from the time, parse it, build the batch with `addBatch` plus the start time, print the reply and warnings; `list`, `rm`, then `pause`/`resume`. |
| `internal/cli/queue.go` | `queue add --at`; `cmdWatch` (`:543`) passes the time to `ClaimNext`, wakes at the earliest `not_before` instead of only on its poll interval, and materializes due recurring occurrences before claiming. |
| `internal/status` | `scheduled` state, `scheduled_for` and `schedule` fields. |
| `internal/cli/status.go`, watch view, hub snapshot | Render scheduled and held lanes. |
| `internal/history`, `internal/chain` | `schedule`, `schedule-start`, `schedule-rm` (and phase two `schedule-paused`, `schedule-superseded`) events; `scheduled_for` on round entries. |
| `internal/config` | `schedule.tz` default. |
| `internal/doctor` | Warn about scheduled batches when no worker runs, about recurring templates expiring within 3 days, and about paused templates. |
| `internal/help`, README, `swimlane.md` | `schedule` help next to `queue`; the planner workflow's step 9 mentions scheduling; the brief's out-of-scope line (phase two). |

### Implementation steps

1. **`internal/schedule` parsing and formatting.** A table test covering
   every input form, invalid input, past times becoming tomorrow, DST gaps
   and overlaps in two zones, and the exact reply line. This is pure, so do
   it first.
2. **Queue entry fields and signing.** Add the start-time fields; extend
   `Statement` only when they're set. Prove an existing signed entry still
   verifies byte for byte, and that editing `not_before` voids a signed one.
3. **Claiming by time.** `ClaimNext(root, user, lease, now)` skips entries
   not yet due and orders due ones by start time. Use the queue's
   existing `now` hook for tests.
4. **The downstream hold.** Share the launcher's dependency edges and
   implement `queue.Held`; `ClaimNext` skips held entries. Test the FIFO
   case from *Motivation* explicitly.
5. **`swim schedule` / `queue add --at`**: the reply, the warnings,
   `--yaml`, `list` and `rm`.
6. **Watch.** Wake for the earliest `not_before`; headless watch too.
7. **Visibility.** Status, plan, the watch screen, the hub, and the
   `swim run` refusal.
8. **Record.** `.swim.log` events, `scheduled_for` in the chain, verify
   output.
9. **Docs and doctor.** Ship phase one here.
10. **Phase two: recurring.** Template storage, materializing, expiry
    capped by `standing.max`, pause on manifest change, overlap and miss
    handling, refusing `stub`/`new`, and the brief change.

## Open questions

- **`-y` on a schedule.** Refused here. Is there a real case for it in a
  one-shot, given that `--approve` can name every flag?
- **Whose clock?** The schedule is written in the scheduler's zone, but
  the worker's clock starts it. Workers on other hosts with skewed clocks
  start it early or late by the skew. Should `swim doctor` on a worker check
  clock sync (compare against the hub when enrolled)?
- **Beyond daily.** Weekdays only (`--weekdays`) and named days
  (`--days mon,thu`) are the obvious next forms. Full cron syntax probably
  isn't: it's hard to echo back plainly, and the reply line is the
  safeguard.
- **Hub scheduling.** Should a hub operator be able to schedule or cancel
  through `headless:` commands? Cancel seems safe; scheduling carries
  approvals, so it would need the approver's signature like hub approvals do.
- **Notifications.** Should a late start or a paused schedule chime (`internal/chime`)
  or push to the hub as an alert, not just a `.swim.log` line?

## Testing

- **Unit, parsing** (step 1): every form in the table; `1330`, `13:30:00` and
  `1:30pm` give the same time and the same reply; invalid input is refused;
  a bare past time rolls to tomorrow; DST gap and overlap in
  `America/Chicago` and `Europe/London`.
- **Unit, queue**: a not-yet-due entry isn't claimed, and one behind it for
  other lanes is; at `not_before` it's claimed; hours later (no worker in between) it's
  still claimed and run, with the late start recorded; due entries are
  claimed in start-time order; a signed entry with an edited start time
  is refused.
- **Unit, held**: lane 2 `After: 1`. Schedule 1 at T, then queue 2. Before T,
  `ClaimNext` returns nothing for 2. After 1 passes, 2 runs. After 1 fails, 2
  is skipped. After 1's batch is removed, 2 is skipped with that in its reason.
  A transitive dependency (3 → 2 → 1) is held too.
- **e2e** (`internal/e2e`, with a clock override for the worker):
  `swim schedule 1 <now+3s>` and `swim watch --once` start lane 1 no earlier
  than the time. `swim status` shows `scheduled` before and `passed` after.
  `swim run 1` before the time is refused.
- **e2e, no worker warning** when no watch holds the lock; none when one
  does.
- **Phase two**: an occurrence is made once with two workers racing; editing
  the script pauses the template and skips the occurrence; the expiry
  defaults to 30 days and is capped by `standing.max`; overlap is recorded as
  `skipped: overlap`; after three days with no worker, one occurrence runs and two are `skipped: superseded`; `swim stub` on a recurring
  lane is refused.

## Out of scope

- A daemon. Schedules run only while a worker (`swim watch`, foreground or
  headless) runs. swim doesn't install system timers or launch agents on its
  own.
- Running on the hub. The hub shows schedules; workers start them.
- Cron syntax, sub-daily recurrence, and calendar-aware windows (holidays,
  change freezes).
- Re-pinning a recurring schedule to a new script automatically.
