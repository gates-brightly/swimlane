# Trash a lane: `d` in swim watch, a hub button, swim trash N

Status: proposed

## Summary

Trash one lane, from three places:

- **`swim watch`:** `d` on a lane row.
- **The hub:** a Trash button on a headless worker's lane.
- **The CLI:** `swim trash N|JOB ...`, which the other two are front ends
  for.

The lane's script files go to `.swim/cleaned/`, the same place `swim clean`
puts scripts it sets aside. Its current log is archived, and the lane drops
off the screen. Nothing is deleted: `swim trash --restore` (or `swim clean
--restore`) puts it back, and `swim prune` deletes it for good later, as it
already does for `swim clean`.

```
swim watch · ~/dev/orders
  swim 3   failed   3f2a9c1e  cut over orders to terraform
▸ swim 7   pending  7b4d4e2a  try the old migration path
                                     d → press d again to trash swim 7 (lane.7.sh, lane.7.yml)
                                     d → swim 7 trashed (.swim/cleaned/20261015-1712-lane7/); swim trash --restore 20261015-1712-lane7
```

## Motivation

- **Dead rounds clutter the screen.** A lane that was written and never
  run, or abandoned after a failure, stays in the watch list until someone
  goes to a terminal and runs `swim archive` and `swim stub`, or `rm`.
  For a headless worker, that someone has to log in to the box.
- **`rm lane.7.sh` loses things.** The script is gone, its approvals point
  at a hash nobody can read back, and the log still holds the lane on the
  screen.
- **`swim clean` is all or nothing.** It's the right tool for "start over",
  not for "drop this one lane". It already has everything a single-lane
  version needs: set scripts aside with their hashes, archive logs,
  restore, and prune.
- **Recoverable first.** A key or a button is easy to hit by mistake, so
  this only moves things aside. Hard deletion stays with `swim prune`.

## Behaviour

### What trashing a lane does

1. **Moves the lane's files** to `.swim/cleaned/<stamp>-lane<N>/`, keeping
   their names: `lane.N.*` (any language) and its sidecar `lane.N.yml`.
   The entry has the same layout and manifest as one `swim clean` writes
   (each file's SHA-256), plus the lane, its job id, who trashed it, when,
   and from where (`cli`, `watch` or `hub`).
2. **Archives the lane's current log,** as `swim archive N --as
   trash-<stamp>` would. Logs are never moved or deleted, because `swim
   verify` checks them.
3. **Records it** in `.swim.log` (`trash`: lane, job, stamp, files, from)
   and the chain (a `trash` entry with the moved files' SHA-256s, as `swim
   clean` records its moved scripts).

With no script and no current log, the lane is no longer in use, so it
leaves `swim status`, the watch screen and the hub.

It never touches files the script includes, `swim.yml`, `lookup.yml` or
`approvals/`. Signed approvals of the trashed script stay where they are
and count again if it's restored unchanged.

### Refused

The same rules apply everywhere it can start. Each refusal says why and
what to do instead.

- **A running or paused lane:** `swim interrupt N` first.
- **A lane in a waiting or scheduled batch:** `swim queue rm #K` (or `swim
  schedule rm`) first. A batch is pinned to the script.
- **An Always lane:** refused, as `swim stub` refuses it, unless the CLI is
  given `--force`. Neither the watch key nor the hub button ever forces.
- **A job in a group file** (`lanes.<name>.sh`, `1.4`): group files are
  committed, standing jobs. Edit the file instead.
- **While another swim holds the lane** (a run, a worker): the same busy
  check as `swim clean`.

### In swim watch

- **`d` on a lane row** asks for confirmation on the screen's note line,
  naming the files that will move. **A second `d`** within 5 seconds
  trashes the lane. Any other key, or the timeout, cancels.
- **The note says what happened,** including the restore command.
- **On a queue row,** `d` does nothing in this version and says so
  ("d trashes lanes; swim queue rm #4 removes a batch").
- **Hints:** `d trash` joins the hint line (`↑ ↓ pick  enter output  r run
  now  q queue  d trash  ctrl-c quit`).
- **A headless worker's watch screen** (`swim watch --headless` has none)
  is reached through the hub instead (below).

### On the hub

- **A Trash button** on each idle lane of a headless worker's page, for
  users with the `operator` role. Clicking it opens a confirmation page
  that names the lane, its job, its round and the files that will move.
  Only that page's button sends the command, so one click never trashes
  anything.
- **A relayed command,** like `interrupt` and `run`: kind `trash`, pinned to
  the lane's job id. If the lane holds a different job by the time the
  worker takes the command, it's refused ("swim 7 holds 9c6a1302 now, not
  7b4d4e2a"), so a rewritten lane is never trashed by an old click.
- **Off unless allowed:** the worker takes it only if the repo's
  `swim.yml` lists it: `headless: {allow: [..., trash]}`. Like `run`, it
  isn't in the default list.
- **The worker decides:** it applies the refusals above, records the
  command as it records every hub command, and acknowledges `done` or
  `refused` with the reason, which the hub shows on the lane.
- **Recorded as from the hub:** `.swim.log` and the chain name the hub
  user who asked and the worker that did it.

### The CLI

```
swim trash N|JOB ... [--force] [-n]      move lanes to .swim/cleaned/
swim trash                                list trashed lanes (stamp, lane, job, round, when, who, from)
swim trash --restore STAMP                put one back
```

- **`-n`:** print what would move, and change nothing.
- **`--restore`:** is the same restore `swim clean --restore` does, so
  either command takes either kind of stamp. It's refused if `lane.N.*` is
  in use again. It doesn't restore the archived log: the restored script is
  pending, as it was before its last run. It's recorded as `untrash` in
  `.swim.log` and the chain.
- **Without a terminal,** `swim trash N` just does it (it's recoverable).
  At a terminal, it asks once for several lanes.
- **`swim prune`** needs no change: `--older D` and `--stamp` already cover
  everything under `.swim/cleaned/`.

## Design

| Area | Change |
|---|---|
| `internal/cli/trash.go` (new) | `cmdTrash`: resolve lanes, the refusals above, move, archive, record; list; `--restore` (calls `cleanRestore`). Built on `swim clean`'s pieces: `cleanedDir` (`clean.go:39`), `archiveLane` (`:371`), `moveScripts` (`:428`), `cleanRestore` (`:455`). One function, `trashLane(root, cfg, n, job, by, from)`, does the work for all three front ends. |
| `internal/cli/clean.go` | A cleaned entry's manifest gains optional `lane`, `job`, `by`, `from`. `swim clean --restore` and its listing show trash entries too. |
| `internal/cli/watchview.go` | `d` in `watchKeys` (`:35`) and the hints. `act` gets a confirm state (the pending lane and a deadline), so a second `d` within 5s calls `trashLane`. |
| `internal/hub/commands.go`, `internal/hub/pages.go` | `KindTrash` with a job pin and the operator role. The confirmation page, and the button on idle lanes. |
| `internal/cli/hubcmds.go` | The worker side: check `headless.allow`, the job pin and expiry, call `trashLane` with `from: hub` and the hub user, acknowledge. |
| `internal/history`, `internal/chain` | `trash` and `untrash` events and entries. |
| `internal/help`, guide | `swim trash` beside `stub` and `archive`; WATCH lists `d`; `headless.allow` lists `trash`; the hub section mentions the button. |

## Open questions

- **Name:** `swim trash` or `swim rm`? `trash` says it's recoverable.
- **The live view of `swim run`:** should `d` work there too, for lanes not
  in the run? Leaning no, because the run's screen is about the run.
- **Restore from the hub?** It would need the same pin and allow rules.
  It's left to the CLI for now, since a restore is rare and you'd want to
  look at the script first.

## Testing

- **e2e, CLI:**
  - Trash a pending lane. Its files are in
    `.swim/cleaned/<stamp>-lane<N>/` with matching SHA-256s, the log is
    archived, `swim status` no longer lists it, and `swim verify` passes.
  - Restore it with `swim trash --restore`, and again with `swim clean
    --restore`. The script is back byte for byte, pending, and a signed
    approval of it counts again.
- **e2e, refusals:** a running lane, a lane in a queued batch, an Always
  lane without `--force`, a group-file job.
- **e2e, prune:** `swim prune --stamp <trash stamp> --confirm` deletes the
  entry and records it.
- **PTY, watch:** `d` then `d` trashes the selected lane and the row
  disappears. `d` then another key, or waiting past 5s, changes nothing.
- **e2e, hub:**
  - With `trash` in `headless.allow`, an operator's trash command is done
    and the lane leaves the worker's page.
  - Without it in the list, the command is refused.
  - A viewer can't send it.
  - A command pinned to an old job, after the lane was rewritten, is
    refused and nothing moves.
  - `.swim.log` names the hub user.

## Out of scope

- Hard deletion from the watch screen or the hub.
- Removing queue batches with `d`.
- Trashing the files a lane includes, or anything outside its own
  `lane.N.*` files.
