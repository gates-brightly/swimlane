# The approval socket (swim run's authority)

How a lane's swim processes ask `swim run` to decide a guard, use an
approval, and learn which lane they belong to. Both ends are swim: the
launcher serves it, and the hidden commands a lane calls (`swim _approve`,
`swim _mark`, `swim guard`, ...) use it. A lane program never speaks it
directly; the SDKs speak [`swim.lane/v1`](../../lane/v1/README.md).

Not to be confused with `swim.approval/v1`, the schema of the signed
approvals files under `approvals/`. Like every hidden command, this
protocol is experimental (see [compatibility](../../../../docs/compatibility.md)):
both ends are always the same swim binary.

## Transport

- `swim run` listens on a Unix socket, mode `0600` in a `0700` temp
  directory, and passes its path to every lane as `SWIM_APPROVAL`.
- Each lane process (each matrix cell or job) gets its own random token,
  `SWIM_APPROVAL_TOKEN`, registered with what the launcher knows of it: the
  lane, cell, job, run, the manifest it runs and the guards its script
  declares. The token is bound to the process once it starts, and revoked
  when it exits.
- **The process tree:** where the platform can tell (Linux `SO_PEERCRED`,
  macOS `LOCAL_PEERPID`), a request must come from the lane's process or
  one of its descendants. A token read from another lane (from
  `/proc/<pid>/environ`, say) is refused, and so is a process that left its
  lane's tree (daemonized, reparented to init). Elsewhere the token alone
  decides.
- **One request per connection:** a JSON object on one line, at most 1 MiB,
  sent within 10 seconds. A larger, malformed or slower request is answered
  with a decline (`request too large`, `malformed request`, `the request
  took too long to arrive`) and the connection is closed.

## Requests

```json
{"token": "…", "flag": "ALLOW_DROP", "desc": "drop table orders", "lane": 1, "job": "…"}
{"token": "…", "flag": "ALLOW_DROP", "desc": "drop table orders", "flag_set": true}
{"token": "…", "op": "consume", "id": "…", "flag": "ALLOW_DROP"}
{"token": "…", "op": "whoami"}
```

- **Ask** (no `op`): decide guard `flag`. `lane` and `job` are what the
  lane claims; the launcher ignores them and uses what it registered for the
  token. `desc` is the lane's own words, shown labelled as the lane's
  (`the lane says: …`) with escape sequences and control characters
  removed. `flag_set`: the lane's environment has `flag=1`.
- **Consume:** use the approval `id` (from an approved answer), once, for
  `flag`, with the token it was given to. The launcher records the
  approve entry in the chain now, and answers its hash.
- **Whoami:** which lane, cell, job and run the token is for.

## Answers

```json
{"approved": true, "id": "…", "lane": 1, "job": "…", "how": "approved by swim run -y (zach)", "observed": {"tick": "0"}}
{"approved": false, "dry": true, "why": "ALLOW_DROP=1 was set inside the lane, not in the environment swim run started with"}
{"approved": false, "by": "zach"}
{"approved": true, "id": "…", "hash": "…"}
{"approved": true, "lane": 1, "cell": "orders", "job": "…", "run": "r-…"}
```

`approved: false` with `dry: true` means no one was asked: the guard is a
dry run, and `why` (if set) says why nothing else approved it. `how` is the
detail the lane logs with `APPROVED`.

## How the launcher decides

In this order, from what it saw when the run started:

1. **`flag_set`:** approved only if the launcher's own environment (the one
   `swim run` was started with, or a queued batch's approvals) has
   `flag=1`. A flag set inside the lane, by its env files or by anything it
   runs, is declined: `FLAG=1 was set inside the lane, not in the
   environment swim run started with`.
2. **Signed approvals:** `approvals/` and `lookup.yml` as they were when
   the run started; files a lane writes later don't count.
3. **The operator:** a pre-answer in the live view, `-y`, or the prompt,
   when someone can be asked. A guard the script doesn't declare is refused
   before any of this (except under `flag_set`: the operator named that
   flag).
4. Otherwise a dry run.

Every request is declined when the lane hangs up, the run is stopping, or
the round's `Timeout` runs out. An unknown or revoked token, a process
outside the lane, an undeclared guard and a reused or foreign `id` are
refused, shown in the live view and logged in `.swim.log`.

## The record

The launcher, not the lane, writes each approve entry, when the lane
consumes the approval. Once the run's last lane exits, it appends a `run`
entry: the manifest and script each lane ran, every approve entry it
recorded and the guard flags its environment approved. `swim verify`
fails on an approve entry its run's attestation doesn't list, a round whose
script or manifest isn't the one attested, and a run attested twice.
