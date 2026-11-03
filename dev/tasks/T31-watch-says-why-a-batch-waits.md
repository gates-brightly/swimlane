# T31. `swim watch` says why a batch waits (P2, gap)

Source: the UX review (item 9: put new work into the queue and log loop).

**Today:** `laneBusy` (internal/cli/watchbatches.go:233) knows why a lane
can't start: its script is running, or another swim run has it waiting,
queued or locked. The scheduler uses it to skip a batch
(watchbatches.go:216), `r` on a waiting batch says `not now: <why>`
(internal/cli/watchview.go:872), and `swim wait` prints `waiting: <why>`
(internal/cli/queue.go:553). Check first whether a waiting batch's row on
the watch screen and in `swim queue` shows it; neither appeared to.

**Do:**
- A waiting batch's row on the watch screen ends with why it waits, from
  the same `laneBusy` the scheduler uses: `waits: swim 2 is running (pid
  4121)`, `waits: swim 3 holds lock tf/orders`, or `waits: an older batch
  has swim 4`.
- `swim queue` (list) shows the same reason under a waiting entry, and
  `--yaml` carries it as `waits:`.

**Done when:** an e2e test queues a batch whose lane another run holds,
and both `swim queue` and the watch screen (a pty test, as the watch
screen tests do) name that lane and why.
