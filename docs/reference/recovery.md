# Recovery

When something goes wrong: what you see, why, and the commands that fix
it. Start with `swim status` and `swim doctor`; they name most of these.

**Before you change anything,** read the lane's log (`swim log N`) and
check the target itself. A log says what swim saw; only the target says
what actually changed. Rounds should be safe to rerun, but a step that was
cut off halfway may have left work half done.

## An orphaned queue entry

**You see:** `swim queue` lists an entry as
`orphaned: its watcher is gone (swim queue requeue N)`.

**Why:** a `swim watch` claimed the batch and then died (killed, a crash,
the machine went down) before it finished. swim never runs an orphaned
batch again on its own.

**Fix:**

```sh
swim queue show ID          # the entry: its lanes, approvals, and the watcher that claimed it
swim log N                  # for each lane in it: how far did it get?
swim queue requeue ID       # run it again: back of the queue, same lanes, pins and approvals
swim queue requeue ID --failed   # after a fix: only the lanes that didn't pass, pinned as they are now
swim queue rm ID            # or drop it
```

`ID` is the entry's id, a 4+ character prefix of it, or its `#` number. A
requeued lane whose script or env files changed since it was queued is
skipped, as always.

A batch claimed on **another machine** (a queue on a shared filesystem)
shows as orphaned once its watcher's heartbeat is older than
`queue_lease` (default 2m). A hung machine's batch may still be changing
things: check that host (`swim queue show ID` names it and the pid) before
you requeue.

## An interrupted round

**You see:** a lane `interrupted` in `swim status`, `== END INTERRUPTED` and
`STOP  interrupted by operator (after: <step>)` in its log; lanes that hadn't
started are `skipped`.

**Why:** Ctrl-C, `swim interrupt N`, a CI cancel (SIGTERM), or a closed
terminal (SIGHUP).

**Fix:**

```sh
swim log N                  # the last step that ran, and whether it finished
swim status                 # interrupted and skipped rounds are what swim all retries
swim all                    # run them (or: swim run N for one lane)
```

A queued batch that was interrupted is consumed as `interrupted`; run it
again with `swim queue requeue ID`.

## A round that failed partway through a change

**You see:** a FAIL or STOP in a round's `change` stage, after some of its
changes passed. The target is now half-changed.

**Fix:** work it out before writing anything.

1. From `swim log N`: which change steps passed, which failed (exit code,
   output), whether a guard was approved.
2. The before state is in the round's snapshots (`saved:` lines).
3. Don't rerun blindly: only safe if its gates tell "already done by us"
   from "not done" (resources tagged with `$SWIM_ROUND_ID`).
4. Write a recovery round: snapshot what's deployed now, gate on it, then
   finish forward or roll back behind its own guard.
5. Next time, write the rollback as its own guarded lane before the
   change runs, and gates that make the round rerunnable.

## A lane stuck `running`

**You see:** `swim status` shows `interrupted (its process is gone)`, or a lane says
`running` long after anything could be running. `swim doctor` reports
`[stale-pid]`. Or `swim run` / `swim watch` starts with
`lane N: its process died without ending its round (pid P); recorded it
as interrupted`.

**Why:** the lane's process died without running its traps: a closed
terminal, SIGKILL, a crash, a reboot. The next `swim run`, `swim all`,
`swim ci` or `swim watch` ends such a round by itself, as
[state](state.md#lane) describes, and never reruns anything.

**Fix:**

```sh
swim doctor                 # [stale-pid] .swim/laneN.pid names process P, which is gone
swim doctor --fix           # removes stale pid files (and other leftover state)
swim status --rebuild       # rebuilds status from the logs: the lane is now interrupted
swim log N                  # what it did before it died
swim run N                  # rerun it when you're sure it's safe
```

A dead pid file doesn't block a rerun; the rebuild just makes `swim
status` tell the truth sooner. A lane running on another machine sharing
the repo is never taken for dead here: if that machine is gone for good,
`rm .swim/laneN.pid` and `swim status --rebuild`.

## `swim verify` fails

**You see:** `NOT VERIFIED`, usually with `BROKEN:` lines, and exit 1. The
common ones:

- `entry #N (line L): prev hash doesn't match ...` or `seq ...`: an entry
  was changed, removed, inserted or reordered.
- `signature doesn't match the entry`: an entry was changed after it was
  signed.
- `lane N's log for run R was changed after the round ended`: a lane log
  was edited.
- `the kept script <sha>.sh was changed`: a copy under `.swim/scripts/` was
  edited.

`warning:` lines (unsigned entries, a missing script copy, a deleted log)
don't fail it; `swim verify --strict` does.

**Fix:** treat it as evidence, not as a bug to clear.

```sh
mkdir -p ~/swim-evidence
cp -R .swim/chain.jsonl .swim/logs .swim/scripts ~/swim-evidence/    # keep a copy first
tail -5 .swim/chain.jsonl   # what the chain says happened last
swim note "swim verify: <what broke>; copy in ~/swim-evidence"
```

- **Don't edit `.swim/chain.jsonl`** to make it pass. Every later link and
  signature depends on the bytes before it, so an edit breaks more than it
  fixes and destroys what you need to find out what happened.
- **An edited log or script copy:** put back the original if you have one
  (a backup, the copy you just made from another machine); verify checks
  the bytes again.
- **Starting over:** if you need a clean chain, move the broken one aside
  (`mv .swim/chain.jsonl ~/swim-evidence/chain.broken.jsonl`); the next
  round starts a new chain at entry 1. Keep the old file and the note.

## A lost runner key

**You see:** `signing: required, but ...` when a run or round starts
(`... didn't vouch for a run key`), or (under `signing: auto`) new chain
entries recorded unsigned, which `swim verify` warns about.

**Why:** the key in `signing_key` (default `~/.ssh/id_ed25519`) is gone, or
needs a passphrase and no ssh-agent holds it; or it asks for each use
(`ssh-add -c`, a security key) and nobody said yes or touched it as the
run started ([the runner key](trust-model.md#the-runner-key)).

**Fix:**

```sh
ssh-keygen -t ed25519 -f ~/.ssh/swim_runner     # a new key (or use another you have)
swim config signing_key ~/.ssh/swim_runner      # a .pub path signs through ssh-agent
swim keys                                       # check: this machine signs with ...
swim verify                                     # old entries still verify
```

- **Old entries still verify.** Each chain entry stores the public key that
  signed it, and `swim verify` checks each signature against that key, so
  losing the private key doesn't break the record.
- **Stolen:** whoever holds the old key could have rewritten the chain
  and re-signed it, which `swim verify` can't tell from the real one.

## A file from a newer swim

**You see:** a command stops with
`lane.N.sh uses swim syntax 5, but this swim (...) reads up to syntax 4`
(or the same for a lane log under `.swim/logs/`).

**Why:** the file was written by a newer swim, in a syntax this one can't
read.

**Fix:** update swim, then run again:

```sh
go install github.com/gates-brightly/swimlane/cmd/swim@latest
swim --version
just which                  # in a swimlane checkout: every swim on PATH, and which one runs
```

## See also

- `swim doctor --help`: every check, and what `--fix` repairs.
- [State machines](state.md): every state a lane and a queue entry can be
  in.
- [Trust model](trust-model.md): what `swim verify` proves and what it
  doesn't.
