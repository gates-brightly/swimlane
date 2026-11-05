# Queue

Hand a ready round to a machine that runs it: queue a batch, and a swimmer
(`swim watch`) runs it when its lanes are free.

```sh
swim queue 4 5 --approve FLAG --decide ROLLOUT=2 --note "cutover"   # a batch
swim queue add                          # every pending lane, as one batch
swim queue                              # waiting, running, orphaned, done
swim queue show|rm|requeue ID
swim queue 6 --wait --timeout 30m       # queue, then block for its result
swim watch [--once] [-y] [--approved]   # the swimmer
swim wait [--timeout D]                 # until nothing runs or is queued
swim wait [--any] BATCH|JOB ...         # until those end (--any: the first); exits with the result
```

## Batches

- **Fixed when queued:** each lane is pinned to its job and exact script; a
  lane edited since, or whose env changed, is skipped. `# After:` between
  its lanes holds.
- **Approvals travel with it** (`--approve`, `--decide`, `-y`); the
  swimmer's environment approves nothing. The batch is signed by whoever
  queued it, so standing approvals for that person apply.
- **A window:** queue now, start `swim watch --once` when it opens (by
  hand or cron).

## The swimmer

- Runs batches side by side as their lanes come free: none in another
  batch or run. Oldest first, but a later batch whose lanes are free goes
  ahead. Up to `--batches N` (`watch_batches`, 4) at once.
- **Asks for guards on its screen:** a guard nothing else approved waits
  under a yellow `Waiting for you` bar; `y`/`n` answers it (a decision takes
  its number). Without the screen (`--no-tui`, not a terminal) a batch
  never asks: its guards dry-run.
- **On a terminal**, it lists every lane's job and the queue: `r` runs one
  now, `q` adds it to the next batch and `s` submits it, `Q` queues every
  job that hasn't passed, `l` shows logs, `Enter` follows a batch or lane,
  `o` hides the output stream. `(~)` marks a lane that changed since you
  last acted on it.
- **Several swimmers** can share one queue: claiming is atomic, and a
  running batch renews a heartbeat (`queue_lease`, 2m), so from another host
  a dead swimmer's batch shows as orphaned (see
  [recovery](recovery.md#an-orphaned-queue-entry)).
- `--approved` also queues scripts once they're fully approved. Ctrl-C
  stops the batches in hand gracefully. Each finished batch goes to
  `.swim/queue/done/` with its result.
