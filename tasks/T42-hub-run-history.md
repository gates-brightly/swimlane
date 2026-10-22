# T42. Workers report finished runs; the hub keeps them (P1, hub, gap)

Part of [swim.plan.md](../swim.plan.md) Phase 2.

**Today:** the hub keeps only each worker's latest snapshot, in memory
(`internal/hub/hub.go`, `hubSnapshot` in `internal/cli/hub.go:594`). A run
that ended is gone from it once the lanes are archived or run again, so
there's nothing to browse.

What already exists to send:
- `.swim/runs/<run>.yml` (`internal/runrec`): every lane in the run, its
  `After` (the run's graph), timings, state and exit.
- `logparse.Doc` (`swim log --yaml`, `internal/logparse/doc.go:37`): a
  round's stages, steps, commands, exits, durations and output. Its
  fields carry yaml tags only; JSON needs tags too, the same names.

**Do:**
- Worker: when a run record appears (or changes) under `.swim/runs/`,
  `PUT /api/workers/{id}/runs/{run}` with the record and each lane's Doc
  (masked by the worker, as logs are). Keep a sent-marker so a restarted
  `swim watch` sends only what the hub hasn't acked; a hub with a new
  epoch gets the last N again.
- Hub: keep them under `--state DIR/runs/<worker>/<run>/` (`run.json`,
  `lane.N.json`), sanitized (`ui.Safe`) as snapshots are. An index per
  worker for listing newest first.
- Retention: `--retain 30d` and `--retain-bytes 2G` (oldest runs go
  first), enforced on write and at start.
- Move the snapshot builder out of `internal/cli/hub.go` into
  `internal/hub` (or a `hubreport` package) while here.

**Done when:** a worker that runs lanes 1-3 shows that run on the hub
after a hub restart and after the lanes are archived; a run is sent once
(acked), and again after the hub's state is wiped; the byte limit drops
the oldest run (tests for each).
