# Recovery

When something goes wrong: what you see, why, and the commands that fix
it. Start with `swim status` and `swim doctor`; they name most of these.

[← back to using swim](usage.md#reference) ·
[State machines](state.md) · [Trust model](trust-model.md)

**Before you change anything,** read the lane's log (`swim log N`) and
check the target itself. A log says what swim saw; only the target says
what actually changed. Rounds should be safe to rerun, but a step that was
cut off halfway may have left work half done.

## An orphaned queue entry

**You see:** `swim queue` lists an entry as
`orphaned: its swimmer is gone (swim queue requeue N)`.

**Why:** a `swim watch` claimed the batch and then died (killed, a crash,
the machine went down) before it finished. swim never runs an orphaned
batch again on its own.

**Fix:**

```sh
swim queue show ID          # the entry: its lanes, approvals, and the swimmer that claimed it
swim log N                  # for each lane in it: how far did it get?
swim queue requeue ID       # run it again: back of the queue, same lanes, pins and approvals
swim queue rm ID            # or drop it
```

`ID` is the entry's id, a 4+ character prefix of it, or its `#` number. A
requeued lane whose script or env files changed since it was queued is
skipped, as always.

A batch claimed by a swimmer on **another machine** (a queue on a shared
filesystem) can't be checked by its process. Instead, the swimmer renews a
heartbeat in the entry every third of its lease (`queue_lease`, default
2m) while the batch runs, and the entry shows as orphaned once the
heartbeat is older than the lease. Before you requeue it, make sure that
swimmer really stopped (a hung machine's batch may still be changing
things); `swim queue show ID` names its host and pid. A claim by an older
swim has no heartbeat and shows as `running?`: check that swimmer, and run
`swim queue requeue` or `rm` on the host that claimed it, where the entry
shows as orphaned once its swimmer is gone.

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

## A lane stuck `running`

**You see:** `swim status` shows `running?` (`process gone`), or a lane says
`running` long after anything could be running. `swim doctor` reports
`[stale-pid]`. Or `swim run` / `swim watch` starts with
`swim N: its process died without ending its round (pid P); recorded it
as interrupted`.

**Why:** the lane's process died without running its traps: a closed
terminal, SIGKILL, a crash, a reboot. `.swim/status.yml` still says
`running`, and `.swim/laneN.pid` names a process that's gone (or a pid the
kernel has since given to another process: the start time in
`.swim/laneN.claim` tells them apart).

The next `swim run`, `swim all`, `swim ci` or `swim watch` ends such a
round by itself: the step that was cut off is recorded as failed, the log
gets `END INTERRUPTED` (exit 137) and the chain a round entry. It never
reruns anything. Read the log before you run the lane again.

**Fix:**

```sh
swim doctor                 # [stale-pid] .swim/laneN.pid names process P, which is gone
swim doctor --fix           # removes stale pid files (and other safe local repairs)
swim status --rebuild       # rebuilds status from the logs: the lane is now interrupted
swim log N                  # what it did before it died
swim run N                  # rerun it when you're sure it's safe
```

A dead pid file doesn't block a rerun on its own; the rebuild (or the
next run) just makes `swim status` (and the agents reading it) tell the
truth.

## `swim verify` fails

**You see:** a level that says `no` (`consistent`, `trusted`, `anchored`
or `authorized`), usually with `BROKEN:` lines, and exit 1. The common ones:

- `entry #N (line L): prev hash doesn't match ...` or `seq ...`: an entry
  was changed, removed, inserted or reordered.
- `signature doesn't match the entry`: an entry was changed after it was
  signed.
- `swim N's log for run R was changed after the round ended`: a lane log
  was edited.
- `the kept script <sha>.sh was changed`: a copy under `.swim/scripts/` was
  edited.
- An `approvals/` file with a broken event.
- `trusted  no`, `N signatures by SHA256:..., a key not in trust.yml`: the
  chain was signed by a key `trust.yml` doesn't list. A new runner key that
  was never added (`swim keys trust --sign`, below), or a chain re-signed
  wholesale.
- `trusted  no`, `trust.yml changed since it was pinned`: `trust.yml` was
  edited without a signed change. Find out who changed it (`git log -p
  trust.yml`); if the change is right, `swim keys pin` accepts it (on each
  machine), or redo it with `swim keys trust KEY --sign`.
- `anchored  no`, `chain truncated or replaced after checkpoint seq N`: the
  local chain no longer holds the entry an anchor recorded. Entries were
  removed from its end, or it was replaced. The anchor's copy (`swim chain
  export`) is the record: `swim verify --against file:/path`.
- `authorized  no`: an approval didn't meet `lookup.yml`'s rule for its
  guard (the `BROKEN:` line names the entry and what was missing).

`warning:` lines (unsigned entries, a missing script copy, a deleted log, a
signer not in `lookup.yml` without a `trust.yml`, an unreachable anchor, an
approval checked against a `lookup.yml` that changed since) and the levels
that say `n/a` or `partial` don't fail it; `swim verify --strict` does.

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
  round starts a new chain at entry 1. Keep the old file and the note. An
  anchor records it: the new chain fails `anchored` against the old
  checkpoints, and anchors refuse checkpoints that go back. To start the
  record afresh, point `chain_anchors` at a new destination (keep the old
  one).

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
$EDITOR lookup.yml                              # add the new public key to your user
swim keys trust ~/.ssh/swim_runner.pub --sign --key ~/.ssh/other_trusted_key   # with a trust.yml
swim keys                                       # check: this machine signs with ... (you)
swim verify                                     # old entries still verify
```

- **Old entries still verify.** Each chain entry stores the public key that
  signed it, and `swim verify` checks each signature against that key, so
  losing the private key doesn't break the record.
- **Lost, not stolen:** keep the old public key in `lookup.yml`, so old
  entries and approvals still name you.
- **With a `trust.yml`,** the change adding the new key must be signed by
  a key it already lists (another approver's, or the old key if you still
  have it). With none left, edit `trust.yml` and run `swim keys pin` on
  each machine (and update CI's `--trust-pin`): that's the decision a
  signed change would have recorded.
- **Stolen:** remove the old key from `lookup.yml`, and from `trust.yml`
  (`swim keys trust OLD.pub --remove --sign`, then `swim keys pin` to stop
  trusting what it signed before). Approvals it signed stop counting:
  re-approve with the new key. Whoever holds the old key could have
  rewritten the chain and re-signed it: `trusted` catches a new key,
  and `anchored` catches a rewrite of anything a checkpoint left the
  machine with (`swim verify --against` an exported copy).
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
just which                  # in a swimlane checkout: every swim on PATH, and which one runs
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
