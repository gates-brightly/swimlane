# Recovery

When something goes wrong: what you see, why, and the commands that fix
it. Start with `swim status` and `swim doctor`; they name most of these.

[← back to the README](../README.md#reference) ·
[State machines](state.md) · [Trust model](trust-model.md)

**Before you change anything,** read the lane's log (`swim log N`) and
check the target itself. A log says what swim saw; only the target says
what actually changed. Rounds should be safe to rerun, but a step that was
cut off halfway may have left work half done.

## An orphaned queue entry

**You see:** `swim queue` lists an entry as
`orphaned: its worker is gone (swim queue requeue N)`.

**Why:** a `swim watch` claimed the batch and then died (killed, a crash,
the machine went down) before it finished. swim never runs an orphaned
batch again on its own.

**Fix:**

```sh
swim queue show ID          # the entry: its lanes, approvals, and the worker that claimed it
swim log N                  # for each lane in it: how far did it get?
swim queue requeue ID       # run it again: back of the queue, same lanes, pins and approvals
swim queue rm ID            # or drop it
```

`ID` is the entry's id, a 4+ character prefix of it, or its `#` number. A
requeued lane whose script or env files changed since it was queued is
skipped, as always.

A batch claimed by a worker on **another machine** shows as `running`, not
orphaned: swim can't see that machine's processes (T6, pending). Check that
worker; `swim queue requeue` and `swim queue rm` refuse a running entry, so
run them on the host that claimed it (`swim queue show ID` names it), where
the entry shows as orphaned once its worker is gone.

## An interrupted round

**You see:** a lane `interrupted` in `swim status`, `== END INTERRUPTED` and
`STOP  interrupted by operator (after: <step>)` in its log; lanes that hadn't
started are `skipped`.

**Why:** Ctrl-C, `swim interrupt N`, a CI cancel (SIGTERM), or a closed
terminal (SIGHUP).

**Fix:**

```sh
swim log N                  # the last step that ran, and whether it finished
swim plan                   # what swim all would run now: interrupted and skipped rounds are retried
swim all                    # run them (or: swim run N for one lane)
swim run N --failed         # a matrix round: only the cells that didn't pass
```

A queued batch that was interrupted is consumed as `interrupted`; run it
again with `swim queue requeue ID`.

## A lane stuck `running`

**You see:** `swim status` shows `running?` (`process gone`), or a lane says
`running` long after anything could be running. `swim doctor` reports
`[stale-pid]`.

**Why:** the lane's process died without running its traps: a closed
terminal, SIGKILL, a crash, a reboot. `.swim/status.yml` still says
`running`, and `.swim/laneN.pid` names a process that's gone.

**Fix:**

```sh
swim doctor                 # [stale-pid] .swim/laneN.pid names process P, which is gone
swim doctor --fix           # removes stale pid files (and other safe local repairs)
swim status --rebuild       # rebuilds status from the logs: the lane is now interrupted
swim log N                  # what it did before it died
swim run N                  # rerun it when you're sure it's safe
```

A dead pid file doesn't block a rerun on its own; the rebuild just makes
`swim status` (and the agents reading it) tell the truth.

## `swim verify` fails

**You see:** `BROKEN:` lines and exit 1. The common ones:

- `entry #N (line L): prev hash doesn't match ...` or `seq ...`: an entry
  was changed, removed, inserted or reordered.
- `signature doesn't match the entry`: an entry was changed after it was
  signed.
- `swim N's log for run R was changed after the round ended`: a lane log
  was edited.
- `the kept script <sha>.sh was changed`: a copy under `.swim/scripts/` was
  edited.
- An `approvals/` file with a broken event.

`warning:` lines (unsigned entries, a missing script copy, a deleted log, a
signer not in `lookup.yml`) don't fail it; `swim verify --strict` does.

**Fix:** treat it as evidence, not as a bug to clear.

```sh
mkdir -p ~/swim-evidence
cp -R .swim/chain.jsonl .swim/logs .swim/scripts approvals ~/swim-evidence/    # keep a copy first
swim history                # what the chain says happened, newest first
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
  round starts a new chain at entry 1. Keep the old file and the note:
  nothing else records that the chain was restarted (T8, pending).

## A lost runner key

**You see:** `signing: required, but ...` when a round starts, or (under
`signing: auto`) new chain entries recorded unsigned, which `swim verify`
warns about.

**Why:** the key in `signing_key` (default `~/.ssh/id_ed25519`) is gone, or
needs a passphrase and no ssh-agent holds it.

**Fix:**

```sh
ssh-keygen -t ed25519 -f ~/.ssh/swim_runner     # a new key (or use another you have)
swim config signing_key ~/.ssh/swim_runner      # a .pub path signs through ssh-agent
$EDITOR lookup.yml                              # add the new public key to your user
swim keys                                       # check: this machine signs with ... (you)
swim verify                                     # old entries still verify
```

- **Old entries still verify.** Each chain entry stores the public key that
  signed it, and `swim verify` checks each signature against that key, so
  losing the private key doesn't break the record.
- **Lost, not stolen:** keep the old public key in `lookup.yml`, so old
  entries and approvals still name you.
- **Stolen:** remove the old key from `lookup.yml`. Approvals it signed
  stop counting: re-approve with the new key. `swim verify` then warns
  about entries signed by a key not in `lookup.yml` (and `--strict`
  fails). Whoever holds the old key could have rewritten the chain and
  re-signed it, and verify can't tell yet (T8, pending): compare with a
  copy you kept elsewhere.
- `swim config signing_key` also sets the key `swim approve` signs with.

## A lock mismatch

**You see:** `swim run` refuses to start, with
`this repo needs a newer swim: .swim.lock pins ...` or
`this repo's lanes were set up for an older swim: ...`. `swim doctor`
reports `[lock]`.

**Why:** `.swim.lock` pins the script, log and state syntax the repo's
lanes use, and this swim reads and writes others.

**Fix,** when the lock is newer than your swim:

```sh
go install github.com/gates-brightly/swimlane/cmd/swim@latest
swim --version
make which                  # in a swimlane checkout: every swim on PATH, and which one runs
```

When the lock is older than your swim:

```sh
swim lock                   # this swim's syntaxes, and the lock's
swim changelog --since <the version in .swim.lock>   # what changed: read the Upgrading notes
swim lock --help            # what each syntax version changed
# update the lane scripts (swim new N writes the current template), let running lanes finish, then:
swim lock --upgrade
```

Commit `.swim.lock`: CI and teammates read it too. A lock in the old form
(`breaking: 7`) means today's syntaxes and runs as it is; `swim lock
--upgrade` rewrites it in the new form.

## See also

- `swim doctor --help`: every check, and what `--fix` repairs.
- [State machines](state.md): every state a lane, job, queue entry and
  approval can be in.
- [Trust model](trust-model.md): what `swim verify` proves and what it
  doesn't.
