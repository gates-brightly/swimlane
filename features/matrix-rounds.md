# Matrix rounds

Status: shipped (unreleased)

## Summary

One lane script runs once per value (a *cell*), each cell with its own log,
status and result:

```bash
# Round:  Cut over $SVC from SST to Terraform
# Matrix: SVC=orders,billing,users,search
# Matrix-Parallel: 2
# Locks:  tf/$SVC
```

```
 swim 3  / running 4:12   Cut over $SVC   2/4 pass · 1 running · 1 waiting
   3[orders]   PASS 1:02
   3[billing]  PASS 1:15
   3[users]    / running 0:40   tf plan
   3[search]   waiting for a slot
```

## Motivation

- **Repeated rounds are the common case.** The migration swim grew out of
  was the same round across about 25 services. Today a planner copies the
  round into 25 lanes, or loops inside one lane where one service's failure
  is buried in a shared log and can't be retried alone.
- **Per-target results matter.** "Billing failed at the DeletionPolicy gate"
  should be one cell's result, retryable without rerunning the other 24.

## Behaviour

### Declaring

- **`# Matrix: KEY=a,b,c`:** each value becomes a cell. Several `Matrix:`
  lines give the cross product (`ENV=staging,prod` × `SVC=...`).
- **Values from a file:** `# Matrix: SVC=@services.txt` reads one value per
  line from a committed file. The file is hashed into the script's SHA, so
  approvals cover the list.
- **Variables:** each key is exported to the cell as an env var
  (`$SVC`), plus `SWIM_CELL=orders` (`ENV=prod,SVC=orders` for cross
  products).
- **Substitution:** `$KEY` in the `Round`, `Locks` and `Outputs` headers is
  replaced per cell.

### Running

- **Concurrency:** `# Matrix-Parallel: N` (default 1) caps cells running at
  once, inside the run's overall `--parallel` limit.
- **`# Matrix-Fail: continue | stop`:**
  - `continue` (default): every cell runs.
  - `stop`: after the first failed cell, cells not yet started are skipped.
- **Lane result:** the lane passes only if every cell passes.
  - **`After: 3`** waits for all of lane 3's cells.
  - **`After: 3[orders]`** waits for one cell.
- **Retrying:** `swim run 3 --failed` reruns only failed and skipped cells.
  `swim run '3[billing]'` runs one cell.
- **Interrupts:** stop every cell, as with lanes.

### Records

- **Logs:** each cell writes `.swim/logs/agent3.orders.log`. `swim log 3`
  shows a cell summary, and `swim log '3[orders]'` shows one cell.
- **Status:** a lane's status gains `cells:`, each with state, counts,
  current step and message ([lane-messages.md](lane-messages.md)).
- **Job ids:** one job for the round, and cells as `<job>/<cell>`, usable
  wherever a job is.
- **The chain:** one round entry per cell, with the cell's log hash and
  result.
- **Approvals:**
  - **Whole round:** a signed approval of the script covers every cell,
    since the matrix is part of the script.
  - **One cell:** `--cell orders` limits an approval to that cell.
  - **Terminal prompts:** each cell asks separately, naming its cell.
- **Plan:** `swim plan` lists the cells, and `--check`
  ([plan-check.md](plan-check.md)) checks each cell.

### Live view

- **Panel:** a lane row shows cell counts, and `→` opens the cell list.
- **Hub:** shows counts and the failed cells.

## Design

- **Header parsing (`internal/lane/script.go`):** `Matrix`,
  `Matrix-Parallel`, `Matrix-Fail`. Expanding cells is a pure function, so
  it's easy to test.
- **Launcher:** the scheduling unit becomes (lane, cell). A plain lane is a
  single cell with an empty name, so today's code paths stay one path. Locks
  are resolved per cell after substitution.
- **Selection:** cell references (`3[orders]`) are parsed wherever lanes
  and jobs are (`laneRef`).
- **Status and display:** `status.Lane.Cells`, plus a cell list view in
  `internal/ui`.
- **Queue:** a batch pins the script SHA, so the cell list is pinned too.

## Open questions (answered)

1. **Limit:** 256 cells. A bigger matrix runs its first 256 and warns.
2. **Exclusions:** yes. `# Matrix-Exclude: ENV=prod,SVC=search` drops every
   cell that matches all the pairs given.
3. **Dynamic values:** no. Approvals must cover a list that exists, so values
   come from the script or a committed `@file`, and both are hashed.
4. **Lane numbers for cells:** no. Cell references (`3[orders]`,
   `<job>/<cell>`) are enough.

Decided while building:

- **How it runs:** a matrix lane stays one lane to the scheduler. Its cells
  run as separate processes with `SWIM_CELL` (and `SWIM_CELL_LOG`) set, so
  the hooks' paths (log, pid, end and msg files) and status writes become
  that cell's: `status.File.Mine`, and `Lane.Cells` entries.
- **Hashing:** `lane.ScriptBytes` is the script plus its `@file` value
  lists. It's what's hashed for the kept script copy, approvals, queue pins,
  pre-answers and checks.
- **Locks:** a lock with `$KEY` is taken per cell, and other locks once for
  the whole lane.
- **No `Job:` line:** the launcher makes one job for the round and passes it
  to the cells (`SWIM_MATRIX_JOB`). `--failed` and `3[cell]` reuse the
  lane's last job, so passed cells are recognised.
- **`After: 3[orders]`** starts once lane 3 finishes, if that cell passed.
  It doesn't start the moment the cell ends.
- **Approvals:** `swim approve N --cell orders` writes
  `lane.N.<sha12>.orders.signature`, whose statement lists the cell. The
  guard hook counts only approvals for the running cell, or for every cell.
- **Live view:** there's no separate cell list. The panel row shows the cell
  counts, output lines carry a `[cell]` prefix, and selecting the lane shows
  the log of the cell running now (or the last that ran). `swim log N`
  lists every cell.

## Testing

- **Expansion:** a single key, a cross product, `@file` values, and
  substitution in headers.
- **Scheduling:** `Matrix-Parallel` respected inside `--parallel`, per-cell
  locks, `Matrix-Fail: stop` skipping cells that haven't started.
- **Dependencies:** `After: 3` waits for all cells, and `After: 3[x]` for
  one.
- **Retry:** `--failed` reruns only failed and skipped cells, and a single
  cell can be run.
- **Approvals:** a script approval covers all cells, `--cell` limits it, and
  editing `services.txt` voids the approval.
- **e2e:** a 25-cell scenario with injected failures and a retry.

## Out of scope

- **Distributing cells across machines.** That's the queue and workers'
  job.
- **Matrix values from secrets.**
