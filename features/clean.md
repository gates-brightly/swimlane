# swim clean and swim prune: a clean slate, then a deliberate purge

Status: proposed

## Summary

Two commands with deliberately different weights:

- **`swim clean`** is everyday and recoverable. It archives every lane's
  finished rounds, so their logs leave `swim status`, the live view, the watch
  screen and the hub. Then it asks two separate questions, both defaulting to
  **no**: remove the waiting queue entries, and move the lane scripts aside?
  It never deletes anything.
- **`swim prune`** is deliberate and permanent. It deletes leftovers older than
  a cutoff you give it: scripts that earlier cleans moved aside, consumed queue
  entries, old run stages, and (only if asked) old archived logs. It records
  what it deleted.

The names follow common conventions. "Clean" gives you a clean slate (`make
clean`). "Prune" removes things nothing needs any more, for good (`git prune`,
`docker system prune`).

```
$ swim clean
swim clean · nothing is deleted · 14 lanes with finished rounds, 3 queued batches, 9 lane scripts

archive  swim 1  passed   j-orders-drop      -> agent1.prev-clean-20261014-1530.log
archive  swim 2  failed   j-cfn-import       -> agent2.prev-clean-20261014-1530.log (+2 job logs)
...
skip     swim 4  Always lane (standing setup; --include-always to clean it)
skip     swim 7  running (pid 81234)

archive 12 lanes' logs? [Y/n] y
archived 12 lanes as clean-20261014-1530

remove 3 waiting queue batches (#12 #13 #15)? [y/N] n
kept the queue

move 9 lane scripts aside (lane.1.sh lane.2.py lanes.deploy.sh ...)?
  they go to .swim/cleaned/20261014-1530/ and can be restored [y/N] y
moved 9 scripts to .swim/cleaned/20261014-1530/ (swim clean --restore 20261014-1530 puts them back)
```

The first line always says `nothing is deleted`, because `git clean` leads
some people to assume "clean" means deletion.

## Motivation

- **Clutter builds up across sessions.** After a few rounds of work, `swim
  status`, the watch screen and the hub list every lane that has ever run.
  The planner in a new session, and the operator, both have to tell current
  work apart from finished work.
- **Clearing it today is one lane at a time.** `swim archive N ...`
  (`cmdArchive`, `internal/cli/commands.go:760`) works per lane and needs a
  name when the log has no job id. `swim queue rm` works per entry, and lane
  scripts are removed by hand. They're git-ignored, so a stray `rm` can't be
  undone.
- **Starting fresh shouldn't cost the audit trail.** Archiving keeps logs
  (`agentN.prev-<name>.log`), so `swim verify`'s log-hash checks and `swim
  history` still work. `swim clean` must keep that, unlike `rm -rf
  .swim/logs`.
- **Leftovers grow without bound.** Consumed queue entries, staged manifest
  copies (`.swim/runs/`) and moved-aside scripts all pile up. They need one
  deliberate way to reclaim the space that never deletes the record. That's
  `swim prune`.

## swim clean

### What gets archived

| Lane | Default | Why |
|---|---|---|
| A finished round (passed, failed, interrupted, stale) with a current log | archived | the point of clean |
| Its job and matrix cell logs | archived with it | `lane.Archive` already moves them |
| Running, waiting or locked | **skipped** | `refuseRunning`: clean never touches live work |
| An Always lane (`# Always: yes`) | **skipped** | standing setup (`refuseAlways`); `--include-always` includes it |
| A pending script that has never run | nothing to archive | its script is covered by the scripts question |

- **One batch name per clean:** `clean-YYYYMMDD-HHMM`, with `-2`, `-3` and so
  on if the name is taken. Every lane archives under it, so `swim history`
  shows one clean and `swim log N --archived clean-...` finds a round again.
  No per-lane naming prompts, even when a log has no job id.
- **All or nothing, like `swim archive`.** Every lane is checked first. If
  any archive target is taken or unreadable, nothing moves, and the command
  lists why.
- **Status reset:** each archived lane goes to idle with `LastArchive` set,
  exactly as `swim archive` does today. A lane with no script left afterwards
  drops out of `swim status`, the live view, the watch screen and the hub
  (`lane.Associated`).

### The queue question

- **What it covers:** only **waiting** entries (`.queue`). It never touches a
  batch that is running (claimed by a worker) or orphaned. Orphans are listed
  with a pointer to `swim queue requeue` and `swim queue rm`, because they
  need their own decision.
- **Removal is `queue.Remove`** for each entry, with one `.swim.log` line per
  entry (`queue rm #12 by clean`).
- **Signed entries:** an entry signed by someone other than the person
  running the clean is listed with its signer. Removing it takes a separate
  confirmation, because that discards someone else's queued, signed work.

### The scripts question

- **What it covers:** lane scripts in every runtime (`lane.N.sh`, `.py`,
  `.ts`, `.go`), group files (`lanes.<group>.sh`), and their `lane.N.yml`
  sidecars.
- **What it never touches:** files named by `# Includes:` (shared
  libraries), `swim.yml`, `lookup.yml`, `approvals/`.
- **Always lanes' scripts are skipped** unless `--include-always`, so this
  repo's `lanes.ci.sh` survives a clean by default.
- **Moved, not deleted:** scripts go to `.swim/cleaned/<stamp>/` with the
  same relative layout, plus a `cleaned.yml` that lists each one with its
  SHA-256. `swim clean --restore <stamp>` moves them back, and refuses any
  path that has been reused since. Only `swim prune` deletes them.
- **Approvals still match.** Approvals and kept script copies
  (`.swim/scripts/<sha>`) are untouched. Approvals bind a script's hash, not
  its path, so a restored, unchanged script still matches its approval.

### Non-interactive use

- **The questions are only asked at a terminal.** Without one (CI, an agent,
  `--no-input`), clean archives and **answers no** to both questions, keeping
  to "fail closed" from the brief.
- **Answering up front:** use `--queue` / `--no-queue` and `--scripts` /
  `--no-scripts`. `--all` is `--queue --scripts`.
- **Not `-y`.** In swim, `-y` means "approve every guard". Reusing it here
  would build `swim clean -y` habits that carry over to `swim run -y`.
- **Previewing and output:** `--dry-run` / `-n` prints the plan and changes
  nothing. `-o yaml` gives the plan, or the result, as `swim.clean/v1`.

### Refusals

- **swim is busy.** Clean refuses while a launcher or worker holds the run
  (`launcher.Busy`): `swim clean: swim run (pid 8123) is running; clean when
  it's done`. Lanes that are merely stale don't block it.
- **A lane is claimed by another session** (once
  [lane-claims.md](lane-claims.md) ships): it's skipped, and the claim's owner
  is named.

### Record

- **History:** one `.swim.log` entry for the clean (`clean
  clean-20261014-1530: 12 archived, 0 queued removed, 9 scripts moved`),
  plus the per-lane `archive` entries that `swim archive` already writes.
  `.swim.log` is only appended to.
- **Chain:** a `clean` entry with the batch name, lanes, the SHAs of moved
  scripts and the ids of removed queue entries. That lets `swim verify`
  explain why logs moved and scripts disappeared.
- **Verify still passes after a clean.** It finds archived logs by their
  hash, through the existing "deleted, not archived" check.

## swim prune

```
$ swim prune --older 30d
swim prune · permanent: deleted files can't be restored

delete  .swim/cleaned/20260901-0915/   4 scripts     12 KB
delete  .swim/queue/done/              31 entries    40 KB   (consumed batches)
delete  .swim/runs/                    57 run stages 3.1 MB  (staged manifests; logs kept)
keep    archived logs                  (pass --logs to include them)

delete these 92 items (3.2 MB)? type "prune" to confirm: prune
pruned 92 items; recorded in the chain as prune-20261014-1530
```

### What it deletes

| Category | Default | What it removes |
|---|---|---|
| Moved-aside scripts | included | `.swim/cleaned/<stamp>/` older than the cutoff |
| Consumed queue entries | included | `.swim/queue/done/*.yml` older than the cutoff |
| Run stages | included | `.swim/runs/<run>/` (staged manifest copies) for rounds older than the cutoff. Never for a round whose log is still current, because verify checks those stages. |
| Archived logs | **opt-in, `--logs`** | `agentN.prev-*.log`, with their job and cell logs, older than the cutoff |
| Kept script copies | **never** | `.swim/scripts/<sha>`: verify needs them for every round still in the chain |
| The chain, `.swim.log`, `approvals/`, `lookup.yml` | **never** | the record |

### Rules

- **A cutoff is required.** Give `--older D` (for example `30d` or `12h`) or
  `--stamp <stamp>`, which prunes one clean batch. A bare `swim prune` prints
  what each category holds and deletes nothing. "Delete everything" is never
  the default.
- **Confirming:** at a terminal you must type `prune`; `y` isn't enough.
  Without a terminal it refuses unless `--confirm` is passed. `--dry-run`
  prints the plan and changes nothing.
- **Pruning logs keeps verify honest.** Before deleting an archived log,
  prune writes a chain entry listing each deleted file and its hash. `swim
  verify` then reports `log pruned on <date> by <user>` instead of "deleted,
  not archived". It also states plainly that it can no longer recheck that
  log's contents.
- **Busy or in use:** prune refuses while swim is busy, like clean. It never
  touches anything belonging to a waiting or running queue entry, or to a
  current round.
- **Doctor:** `swim doctor` reports how much `swim prune --older 30d` would
  free once that passes a threshold, and suggests the command. It never
  prunes on its own.

## Design

| Area | Change |
|---|---|
| `internal/cli/clean.go` (new) | `cmdClean`: build the plan (lanes, queue, scripts), print it, ask, and carry it out. `--restore`. |
| `internal/cli/prune.go` (new) | `cmdPrune`: list each category, filter by the cutoff, show sizes, take the typed confirmation, write the chain entry, and delete. |
| `internal/cli/commands.go` | Move `cmdArchive`'s check-then-move into an `archiveLanes(root, cfg, lanes, name)` helper that `archive` and `clean` both call. |
| `internal/lane` | `Scripts(root)`: every lane script, group file and sidecar, with the Always flag. `CleanScripts(root, stamp, paths)` and `RestoreCleaned(root, stamp)`. |
| `internal/queue` | `List` and `Remove` as today, plus `Done(root)` for prune. |
| `internal/history`, `internal/chain` | `Clean` and `Prune` events and entries. A prune entry lists each deleted log and its hash. |
| `internal/cli/custody_cmds.go` | Verify reads prune entries and reports pruned logs as pruned, not missing. |
| `internal/cli/cli.go` | Register `clean` and `prune`. |
| `internal/help/guide.txt`, README | Document both next to `swim archive`. |
| `internal/doctor` | Report what `swim prune --older 30d` would free, and suggest it past a threshold. |

## Open questions

- **Should archived logs move into a subdirectory?** For example
  `.swim/logs/archive/clean-…/`, instead of staying next to current logs as
  `agentN.prev-*.log`. That would make the logs directory look clean too, but
  it changes where `swim log --archived` and verify look.
- **Outputs:** downstream lanes read outputs from finished rounds (`After:`
  plus `outputs/`). After a clean, a new lane that names a cleaned lane in
  `After:` has nothing to depend on. Should clean warn when a pending script
  depends on a lane it's archiving?
- **Hub:** the hub drops cached events and log tails for this worker on its
  own as the snapshot changes. Is that enough?
- **Manifest stage lifetime:** [execution-manifest.md](execution-manifest.md)
  left open how long `.swim/runs/<run>/` is kept. Is "until `swim prune`,
  and never while the round's log is current" the answer?

## Testing

- **e2e, basic clean.** Set up three finished lanes, one running, one Always,
  two waiting queue entries and one running entry. Then run `swim clean
  --no-queue --no-scripts`:
  - the finished lanes are archived under one batch name;
  - the running lane and the Always lane are untouched;
  - status shows the archived lanes as idle, and `swim verify` passes.
- **e2e, the questions:**
  - `--queue` removes only waiting entries, never the running one.
  - `--scripts` moves scripts and sidecars, but not `Includes:` files or
    Always scripts.
  - `--restore` brings them back, and an unchanged restored script still
    matches its signed approval.
- **e2e, no terminal and dry runs:** with no terminal and no flags, clean
  archives and answers no to both questions. `--dry-run` changes nothing,
  checked by hashing the `.swim/` tree before and after.
- **All or nothing:** with an archive target taken, clean moves nothing.
- **Refusal:** while `swim run` holds the launcher, clean and prune exit
  non-zero and change nothing.
- **Prune:**
  - A bare `swim prune` deletes nothing.
  - `--older` deletes only past the cutoff.
  - Kept script copies, the chain, and current rounds' stages survive.
  - Without a terminal, and without `--confirm`, it refuses.
- **Prune `--logs`:** after an archived log is pruned, `swim verify` passes
  and reports the log as pruned, with the hash from the prune entry.

## Out of scope

- Deleting chain entries, kept script copies, `.swim.log` or approvals. Clean
  only hides; prune deletes only leftovers, and records what it deleted.
- Other projects. Both commands work on the current repo only.
- Cleaning or pruning on a schedule.
