# Headless workers, driven from the hub

Status: proposed

## Summary

`swim watch --headless` runs the queue worker with no terminal at all: in the
foreground under a supervisor, detached into the background, or installed as a
systemd (Linux) or launchd (macOS) service. Everything you would normally do at
the worker's terminal happens on the hub instead:

- answering guard approvals
- interrupting a running lane
- starting a lane or job manually
- reading each job's log

The worker stays the only executor. The hub passes requests to the worker and
displays the result. The worker decides whether to act on each request,
records it, and checks every approval itself.

```sh
swim hub enroll https://hub.internal --token $ENROLL      # once, as today
swim watch --headless                                     # foreground (systemd, docker, tmux)
swim watch --headless --detach                            # background; pid in .swim/headless.pid
swim watch --headless --install systemd                   # writes a user unit, prints how to enable it
swim watch --headless --install launchd                   # writes a LaunchAgent plist
swim watch --headless --status | --stop | --uninstall
```

## Motivation

- **A worker needs someone at its terminal today.** `swim watch` has a live
  view (`internal/cli/watchview.go`), and guards ask on its terminal through
  the approval broker (`internal/approval`). When nobody is there
  (`runEntry` in `internal/cli/queue.go`: "It never prompts"), a guard that the
  batch doesn't approve falls back to a dry run. So unattended workers can only
  run work that was approved in advance.
- **The hub already sees everything but can't act.** Workers push snapshots
  (lanes, queue, log tails, events) every few seconds (`hubReporter`,
  `internal/cli/hub.go`). The operator can see that a guard is waiting, but
  has to open an SSH session to that worker to answer it.
- **Long-lived workers belong under a supervisor.** Running `swim watch` in
  tmux on a box doesn't survive reboots, doesn't restart after a crash, and has
  no clear place for its own logs.

## Behaviour

### Product change this makes

The brief (`swimlane.md`, *Out of scope*) currently says: "Running or
approving from the hub. The hub only shows state." The `internal/hub` package
docs say its pages are read-only. Shipping this feature means changing both.
The rule that replaces them:

> The hub **relays** requests. It never executes anything, never holds
> credentials or secrets, and can't make an approval valid by itself.

A compromised hub can do two things: show false information, and send
requests to workers. Every request is checked by the worker against the same
rules the CLI already applies (signed approvals, `lookup.yml`, script hashes).
Hub access only lets someone do what the per-worker policy below allows.

### Running headless

| | `--headless` | `--headless --detach` | `--install systemd\|launchd` |
|---|---|---|---|
| TTY / live view | none, never reads stdin | none | none |
| Its own output | stdout/stderr (the supervisor keeps them) | `.swim/headless.log` (rotated) | journald / `~/Library/Logs/swim/<id>.log` |
| Restarts | the supervisor's job | none | `Restart=on-failure` / `KeepAlive` |
| Single instance | `.swim/headless.lock` (flock); a second one exits 2 with the holder's pid | same | same |

- **Prerequisite:** the directory must be enrolled with a hub (`.swim/hub.yml`).
  If it isn't, `--headless` refuses to start. Without a hub there would be no
  way to reach the worker.
- **Signals:** SIGTERM and SIGINT behave as they do in `swim watch` today: the
  worker finishes the batch in hand, then exits. A second SIGTERM interrupts
  the running lanes (the same as `swim interrupt`) and exits after their
  handlers run. systemd's `TimeoutStopSec` is set from the longest `Timeout:`
  plus a margin.
- **Config reloads** already happen every loop (`loadCfg`). SIGHUP also
  reopens `.swim/headless.log`, so logrotate works.
- **`-y` and `--approved`** are still allowed, but `--install` refuses `-y`
  unless you also pass `--i-mean-it`. The flag is written into the unit file
  and shown in bold on the hub's worker page. Standing `-y` on a
  service that runs forever is easy to forget is there.
- **`--detach`** has Go re-exec itself with `Setsid` (Go has no fork) and
  writes `.swim/headless.pid`. `--stop` sends SIGTERM to that pid, checking
  first that the process is still swim and still holds the lock.
- **`--install`** writes the unit file and prints the `systemctl --user
  enable --now ...` / `launchctl bootstrap ...` command. It doesn't run that
  command itself. The unit pins the absolute path of the `swim` binary, the
  repo root, and `SWIM_USER`. `--uninstall` stops the service and removes the
  unit file.

### On the hub

The worker page (`/w/{id}`) gets controls. Each one creates a **command** for
that worker:

| Control | Command | The worker does | Who may |
|---|---|---|---|
| Approve / Decline on a waiting guard | `answer` | answers that pending request in the broker | see [Approvals](#approvals) |
| Interrupt lane N | `interrupt` | `swim interrupt N` (pinned to the run id shown) | `operator` role |
| Stop the worker after this batch | `drain` | what SIGTERM does | `operator` role |
| Run lane N / job J now | `run` | `swim queue add N` pinned to the script SHA the page displayed; the queue runs it | `operator` role |
| Open a job's log | `log` | uploads the masked log of that run (see [Logs](#logs)) | `viewer` role |

- **Commands are durable on the hub** (in its `--state` dir). The worker
  acknowledges each one with an outcome: `done`, `refused: <why>`, or
  `expired`. The page shows that outcome next to the button that sent it.
- **Commands expire.** Their lifetime defaults to 10 minutes, and 2 minutes
  for `interrupt`. An expired command is never carried out. So a worker that
  was offline doesn't come back and replay an old "run lane 3".
- **Every command is pinned** to what the page showed when it was sent: the
  run id for `interrupt`, the script SHA for `run`, and the request id plus
  evidence hash for `answer`. If the worker no longer matches, the command is
  refused (`stale: lane 3 is now run r-...`).
- **The worker records every command** it receives in `.swim.log` and the
  chain (`hub <cmd> by <user> via <hub>: <outcome>`). So the worker's own
  record shows what the hub asked for, including refusals.

### Approvals

A guard that's waiting under `--headless` is published in the worker's
snapshot as a pending request. It includes: request id, lane, job, run, flag,
description, the script's SHA, the evidence text, and `evidence_required`. The
worker waits for an answer until the round's deadline. The lowest-common rule
still holds: **if nobody answers, it's a decline**. Hub unreachable, deadline
passed, or worker stopping all mean decline. That matches *Gates and
confirmations fail closed* in the brief.

There are two ways to answer, chosen per repo with `headless.approvals:` in
`swim.yml`:

- **`signed` (the default).** The approver signs the answer with their SSH key,
  either through the CLI (`swim approve --hub <worker>/<request>`) or by
  pasting the output of a `ssh-keygen -Y sign` command that the page
  generates. The statement signed covers: request id, worker id, lane, job,
  script SHA, evidence hash, flag, approve or decline, and expiry. The hub
  just passes the signature along. The **worker** verifies it against
  `lookup.yml` with `internal/sign`, the same way it checks a
  `approvals/*.signature` file. It counts it toward the guard's rule exactly
  like a signed approval. A hub that forges, replays, or redirects a
  signature fails that check.
- **`hub`.** A button click counts as an answer from the hub user who clicked.
  This is equivalent to the terminal prompt today: the decision rests on the
  hub's login, not on a signature. Guards whose `lookup.yml` rule requires
  signers (or `evidence_required`) ignore `hub` answers and keep waiting for a
  signed one. The setting is recorded on every answer (`via hub (unsigned)`)
  and listed in `swim doctor`.

The `Via` field of `approval.Response` names the path the answer took:
`hub signed by alice`, or `hub button by alice`.

### Logs

- **Live tails don't change.** The snapshot already carries `Tail` for running
  lanes.
- **Full logs are fetched on demand, never streamed by default.** The page
  sends a `log` command naming a run. The worker uploads that run's log
  (`.swim/logs/...` or the archive) with `PUT /api/workers/{id}/logs/{run}`,
  in chunks capped at 8 MiB total.
- **The hub keeps uploaded logs in a size-bounded in-memory cache**, never on
  disk by default. The page polls until the log arrives.
- **The worker only uploads logs that are already masked on disk.**
  `internal/redact` runs before anything is written, so this doesn't create a
  new path for secrets to leak. The upload re-applies masking anyway as a
  backstop, using the current secret set.
- `headless.logs: off` in `swim.yml` turns off log fetching for repos that
  must not send logs off the box.

### Hub users and roles

Today the hub has an enroll token, per-worker tokens, and one admin token.
Actions need a person behind them (this is the open question *Hub access* in
the brief):

- `swim hub user add alice --role viewer|operator` (an admin command) issues a
  per-user token. The hub stores only its hash, as it does for worker tokens.
  The browser exchanges the token for a session cookie. Logins and every
  command are written to the hub's audit log (`s.audit`).
- **The user name must match a name in `lookup.yml`**, so a worker can map an
  answer back to a person and to that person's keys. In `signed` mode, the
  user name is only a label. The signature is what counts.
- **Per-worker allow-list:** `headless.allow:` in `swim.yml` (shared, committed)
  limits which commands this repo accepts from the hub. The default is
  `[answer, interrupt, drain, log]`, so `run` must be enabled explicitly. The
  worker enforces the list, so changing it requires a commit, not hub access.

## Design

### Transport: the worker pulls

Workers push today so they can sit behind NAT. Commands keep that: the worker
**long-polls** the hub with its own token. The hub never connects to the
worker.

```
GET    /api/workers/{id}/commands?after=SEQ&wait=25s    worker: commands since SEQ (long-poll)
POST   /api/workers/{id}/commands/{cid}                 worker: ack {outcome, detail}
PUT    /api/workers/{id}/logs/{run}                     worker: a requested log
POST   /api/workers/{id}/commands                       user:   send a command (role-checked)
GET    /api/workers/{id}/logs/{run}                     user:   read it (viewer)
POST   /api/login                                       user token -> session cookie
```

- **Request contents:** a command is `{cid, seq, kind, args, by, at, expires,
  pin}`, where `pin` holds the run id, SHA, or request id and evidence hash
  described above.
- **Ordering:** `seq` increases per worker. The worker keeps the last `seq`
  it carried out in `.swim/hub.yml`, so a hub restart (or a hub replaying
  commands) can't make it repeat one.
- **Fallback:** if long-polling doesn't work through a proxy, `wait=0` falls
  back to the existing report interval.

### Where it lands

| Area | Change |
|---|---|
| `internal/cli/queue.go` `cmdWatch` | `--headless`, `--detach`, `--install`, `--status`, `--stop`, `--uninstall`. Headless implies no TUI and skips `watchViewAllowed`. |
| `internal/cli/headless.go` (new) | Detach/re-exec, the pidfile and flock, the unit and plist templates, the command loop (poll, check, dispatch, ack), and the per-command handlers that call the existing `cmdInterrupt`, `queue.Add`, and the log reader. |
| `internal/cli/queue.go` `runEntry` | In headless mode, start an `approval.Listen` broker for the batch with a hub-backed `Asker`, instead of never prompting. |
| `internal/cli/approve.go` | `hubAsker`: publishes the pending request into the snapshot and waits for a matching `answer` command or the deadline. Signed answers are checked through `sigfile` and `lookup` with the same code path as file approvals. |
| `internal/approval` | `Request` gains `ID`, `ScriptSHA256`, and `EvidenceSHA256`, so an answer can be pinned to one question. |
| `internal/hub/hub.go`, `state.go` | Store commands and users, handle the new routes, roles, and the log cache. The state schema moves to `swim.hub-state/v2`, with migration. |
| `internal/hub/client.go` | `Poll`, `Ack`, and `UploadLog`. |
| `internal/hub/pages.go` | Controls on `/w/{id}`, a pending-approvals panel, a log viewer, and a login page. |
| `internal/hub` `Lane` / `Snapshot` | `Pending []PendingApproval`, and a `Headless` flag that includes the worker's `-y` and `--approved` settings. |
| `internal/config` | A `headless:` block: `approvals: signed\|hub`, `allow: [...]`, `logs: on\|off`, and `command_ttl`. |
| `internal/cli/custody_cmds.go` `cmdApprove` | `--hub WORKER/REQUEST`: fetch the request from the hub, show it, sign the statement, and post it. |
| `internal/doctor` | Checks: service installed or running, lock holder still alive, hub reachable, `approvals: hub` warning, `-y` in the unit file. |
| `internal/help/guide.txt`, README, `swimlane.md` | Document the headless worker and the hub's new role. Replace the *Out of scope* line and close the *Hub access* open question. |

### What stays the same

- **Scripts are still written by the session and committed or placed by
  people.** The hub can't create or edit a lane script. `run` only queues a
  script that is already on the worker, pinned to the SHA the page showed.
- **Hub secret grants** (`internal/hub/state.go`) are still encrypted to the
  worker's key. Nothing in this feature gives the hub plaintext.
- **Chain and history** are written by the worker, as today.

## Open questions

- **Which command takes the flag?** Is it `swim watch --headless`, which this
  spec assumes, or a separate `swim worker` command? `watch` already is the
  worker, but its name suggests a screen.
- **Should the default for `approvals:` be `signed`?** Signing in a browser
  needs a terminal (`ssh-keygen -Y sign`) or the CLI. That's friction, and
  it's deliberate. Is it acceptable?
- **Should `run` from the hub ever carry approvals?** In this spec it never
  does, so its guards ask through the hub at runtime. Should a signed `run`
  be able to act as a signed queue entry, so standing approvals
  (`approvals/auto.<user>.signature`) apply?
- **Log retention on the hub:** in memory only (the proposal), or optionally
  in the state dir with a TTL?
- **More than one headless worker per repo:** the existing queue lock
  (`queue.LockWorker`) already lets only one run at a time. Should the hub
  show the others as standby?
- **Windows service:** out of scope until swim supports Windows lanes.

## Testing

- **Hub unit tests** (`httptest`): command lifecycle (create, poll, ack,
  expire), role checks, `seq` replay rejected, session login, log cache
  bounds, and state v1 to v2 migration.
- **Approval binding** (unit): a signed answer is rejected when any one of
  these differs: request id, worker id, script SHA, evidence hash, flag,
  expiry, or a signer not in `lookup.yml`. `hub` answers never satisfy a
  signed rule. No answer by the deadline means decline.
- **e2e** (`internal/e2e`): start a hub and `swim watch --headless` in a temp
  repo, queue a guarded lane, and check that the pending request appears in
  `GET /api/workers`. Then:
  - answer it signed and check that the lane passes and the chain names the
    signer;
  - repeat with no answer and check for a dry run or decline at the deadline.
- **Interrupt and drain e2e:** interrupt pinned to the current run works.
  Interrupt pinned to an old run is refused as stale. Drain exits after the
  batch.
- **Failure cases:**
  - hub killed while a guard waits: it declines at the deadline, and the
    worker keeps running and re-enrolls when the hub returns;
  - worker killed mid-batch under `--detach`: the lock is released and
    `--status` reports it;
  - two `--headless` in one repo: the second exits 2.
- **Unit and plist golden files** for `--install`.
- **Logs:** a known secret planted in a lane's output never appears in an
  uploaded log, and `headless.logs: off` refuses the request.

## Out of scope

- The hub running anything itself, or holding credentials.
- Creating or editing lane scripts from the hub.
- Pushing commands to workers (inbound connections to workers).
- Federation across hubs, and SSO or OIDC login. Per-user tokens come first.

## Prerequisites

These tasks are in `tasks.md`.

- **T16, TLS.** The hub serves plain HTTP today (`internal/cli/hub.go`).
  Sending answers and logs over it requires TLS, or a refusal to listen on a
  non-loopback address without it.
- **T3 and T10.** Lanes can't mint their own approvals, and the approval
  socket checks a token. Otherwise an answer that arrives from the hub can be
  spoofed locally.
