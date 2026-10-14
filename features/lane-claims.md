# Lane claims: a lease on a lane while a session writes it

Status: proposed

## Summary

A session that starts a round with `swim new N` takes an advisory **claim**
on lane N: a short lease with an owner, a session id and an expiry. While the
claim is live, `new`, `stub` and `archive` from **another** session refuse the
lane unless forced, and `swim status` and `swim plan` show who holds it. The
claim ends when the round starts running (running-lane protection takes over),
when the lane is archived or stubbed, when the holder releases it, or when the
lease runs out.

```yaml
# .swim/claims/lane.3.yml
owner: zach                      # the user .swim.log records
session: planner-7f2c            # SWIM_SESSION, else $USER@host
host: zach-mbp
created: 2026-10-14T09:12:04Z
expires: 2026-10-14T11:12:04Z    # created + lease (default 2h), moved on refresh
job: j-orders-drop
```

```sh
swim new 3 "drop the orders index"     # takes the claim (or refreshes this session's own)
swim lint 3                            # refreshes it, if this session holds it
swim claim 3                           # take or refresh explicitly; --for 4h
swim claim 3 --release                 # give it up
swim claim 3 --break "session died"    # take over a live claim; the reason goes in .swim.log
swim new 3 "..." --force               # override; logged as claim-broken
```

Claims are advisory. They stop swim's own commands from stepping on another
session's round. They can't stop an editor, `cp`, or a worker writing
`lane.3.sh` directly, and they don't reach other machines or worktrees.

## Motivation

- **The brief's first problem.** "Two sessions overwrite each other's
  scripts" (`swimlane.md`, *Problems*). Today the only protections are:
  - **Running-lane protection.** `refuseRunning`
    (`internal/lane/lane.go:671`) refuses to write or archive a lane with a
    live `.swim/laneN.pid`. It covers the run, not the writing before it.
  - **The pending-round check.** `WriteScriptAs`
    (`internal/lane/lane.go:767`) refuses to replace a pending round unless
    `--force`. It only fires once a round is written, it can't tell "my
    round, still being edited" from "someone else's", and `swim stub` always
    passes `force` (`internal/cli/commands.go:868`).
  - **The `Owner:` header.** `swim new` fills it from `$USER`
    (`internal/cli/commands.go:289`). It records authorship, it has no
    expiry, and every session under one account writes the same name.
- **The window that matters.** Between `swim new 3` and `swim run 3` a
  session is writing and linting, often for many minutes. A second session
  that picks lane 3 in that window either gets a pending-round error and
  reaches for `--force`, or (after `swim stub 3`) overwrites the work
  silently. Neither leaves a record of who was there.
- **Recovery when a session disappears.** A session that ends mid-round
  leaves a pending script nobody owns. A lease with an expiry answers "is
  anyone still working on this?" without asking a person.
- **The brief's open question.** `swimlane.md`, *Open questions*: "should a
  worker claim a lane before writing it, beyond the `Owner` header and
  running-lane protection?" This spec answers yes, as an advisory lease.

## Behaviour

### Sessions

- **The session id** is `SWIM_SESSION` when set, otherwise `$USER@host`. An
  agent shell's parent pid isn't stable across tool calls, so it isn't used.
- **Same account, same machine:** without `SWIM_SESSION`, two sessions under
  one account on one machine share an id and don't block each other. That is
  today's behaviour, not a regression. The docs tell planners (and harnesses
  that start them) to export `SWIM_SESSION` per session, and the claim
  message names the variable.
- **Owner** is the name `.swim.log` already records (`SWIM_USER`, `USER`,
  `LOGNAME`). It is shown to people; the session id is what's compared.

### Taking, refreshing and releasing

| Command | Effect on lane N's claim |
|---|---|
| `swim new N` | Takes it (lease default 2h, `claim_lease` in config). If this session already holds it, refreshes it. If another session holds a live one, refuses (below). |
| `swim lint N` | Refreshes it, only if this session holds it. Never creates one, never logs. |
| `swim claim N [--for D]` | Takes or refreshes it explicitly, for a session that edits a lane it didn't create with `new`. |
| `swim run` / `swim all` / `swim watch` starting a lane | Releases it when the lane starts. From there `refuseRunning` protects the lane. Whoever runs it, whatever session holds it. |
| `swim archive N` | Releases it. |
| `swim stub N` | Releases it. |
| `swim claim N --release` | Releases it, if this session holds it (`--force` for another's). |
| expiry | Ignored from then on, and removed by the next command that looks at it. |

A claim is per lane number. A lane in a group file can't be written by
`new` (`WriteScriptAs` already refuses), so `swim claim` refuses group lanes
too.

### Refusing

`swim new`, `swim stub` and `swim archive`, run from a session that doesn't
hold lane N's live claim, refuse:

```
swim 3 is claimed by zach (session planner-7f2c on zach-mbp) until 11:12
(job j-orders-drop); pick another lane, wait, or pass --force
(set SWIM_SESSION to tell sessions apart)
```

- **`--force`** overrides, takes the claim for the new session (`new`) or
  drops it (`stub`, `archive`), and logs a `claim-broken` event:
  `claim-broken  swim 3  job=j-orders-drop  from=planner-7f2c by=planner-91aa via=new`.
  `new`'s and `stub`'s `--force` already override other checks (a pending
  round, an Always lane); one flag covers both and the message names each
  check it overrode.
- **`swim claim N --break "<reason>"`** takes over a live claim without
  touching the script, logging `claim-broken` with the reason. It's the
  explicit path when a session is known to be gone.
- **Archive across lanes** (`swim archive 1 2 3`) keeps its all-or-none
  rule: a claimed lane is one more problem in the list.

### Showing

- **`swim status`:** a claimed lane gets a detail line, next to the
  existing `owner …` line: `claimed by zach (planner-7f2c) until 11:12`. An
  expired claim isn't shown.
- **`swim plan`:** a lane in the plan that another session claims gets a
  warning, not an error (see *Open questions*):
  `swim 3: claimed by planner-7f2c until 11:12; it may still be being written`.
- **YAML:** `swim status --yaml` and `swim plan --yaml` gain an optional
  `claim: {owner, session, host, expires, job}` per lane. It's additive;
  readers that don't know it ignore it.
- **`.swim.log`:** `claim` and `claim-release` events are logged only for
  the explicit `swim claim` command. Claims taken by `new` and released by
  `run`, `archive` or `stub` ride on those commands' existing events, so the
  log doesn't double in length. `claim-broken` is always logged.

### Limits, stated plainly

- **Direct edits aren't stopped.** A claim guards swim's commands. A session
  that writes `lane.3.sh` with its editor tool isn't checked. The help text
  says: claim with `swim new` or `swim claim` before writing.
- **One checkout.** Claims live in `.swim/`, which is git-ignored and per
  worktree. They don't cross machines, worktrees or the hub.
- **`Owner:` stays authorship.** The header records who wrote the round and
  outlives the claim. It isn't changed or checked by claims.
- **The queue is unaffected.** A queued batch already pins its script bytes
  (`internal/queue`); a claim neither blocks nor changes that.

## Design

| Area | Change |
|---|---|
| `internal/lane/claims.go` (new) | `Claim` type; `ClaimDir(root)` = `.swim/claims/`; `TakeClaim(root, n, sess, lease, job, force)`, `RefreshClaim`, `ReleaseClaim`, `ReadClaim` (returns nil for none or expired), `SessionID()`. |
| `internal/lane/lane.go` | `refuseClaimed(root, n, sess)` next to `refuseRunning`, returning an `ErrClaimed` like `ErrRunning`. Not called from `WriteScriptAs` itself (the launcher and `stub` write through it with different rules); the CLI calls it. |
| `internal/cli/commands.go` | `cmdNew`: check and take the claim before `WriteScriptAs`, drop a freshly taken claim if the write fails. `cmdStub`, `cmdArchive`: check (archive in its first, all-or-none pass), then release. |
| `internal/cli/` (new `claim.go`) | `swim claim N [--for D] [--release] [--break REASON] [--force]`. |
| `internal/cli/lint.go` | Refresh when this session holds the claim. Lint stays otherwise read-only: no log, no migration. |
| `internal/launcher` | Release each lane's claim when it starts, after locks are taken. |
| `internal/cli/status.go`, plan | The `claimed by` line, the plan warning, the YAML field. |
| `internal/history` | `Claim = "claim"`, `ClaimRelease = "claim-release"`, `ClaimBroken = "claim-broken"`. |
| `internal/config` | `claim_lease` (duration, default `2h`). |
| `internal/help/guide.txt` | The session rule: claim before writing, set `SWIM_SESSION`. |

### Atomicity

- **Creating:** the claim file is created with `O_CREAT|O_EXCL` (write to a
  temp file, then `link()`, as `history.Append` publishes `.swim.log`), so two
  sessions racing on an unclaimed lane get exactly one winner.
- **Replacing** (refresh, an expired claim, `--force`, `--break`) is
  read-check-rename, serialised by `flock` on **`.swim/claims/.lock`**. The
  planner's sketch put the lock at `.swim/locks/claims.lock`, but that is the
  path of a resource lock named `claims` (`lockPath`,
  `internal/lane/locks.go:32`), so a lane with `Locks: claims` would contend
  with every `swim new`. Keeping it inside `.swim/claims/` avoids the clash.
- **Expiry** is compared against the local clock. Claims are local to one
  checkout, so there's no skew between hosts to handle.

### States

```
         new / claim             lint / new / claim (same session)
 (none) ─────────────▶ claimed ◀──────────────┐
   ▲                    │  │ └────────────────┘
   │   run starts,      │  │ lease runs out
   │   archive, stub,   │  ▼
   └── release ─────────┘ expired ──▶ (none, removed lazily)
                │
                └─ --force / --break by another session: claim-broken, new holder
```

The full lane state machine (idle, claimed, pending, queued, running, done,
archived) goes into `docs/state.md` when that doc is written; this spec only
adds `claimed`.

## Open questions

- **Should `swim run` refuse a lane another session has claimed?**
  (a) **Advisory:** show and warn, run anyway. This spec's default: the
  operator decides, and running releases the claim.
  (b) **Refuse unless `--force`:** safer against running a half-written
  round, but adds a release step to every round, and stalls the operator when
  a session dies before its lease expires.
  *Decision needed from the user.*
- **Lease length.** 2h covers a long round of writing and linting. Is a
  shorter lease with more refreshes (every `lint`, every `claim`) better for
  recovering from dead sessions?
- **Should lint refresh at all?** It keeps an active session's claim alive
  without a new command, but it's a write from a command that is otherwise
  read-only. The alternative is refreshing only on `new` and `claim`.
- **Should `--force` be split?** One flag now overrides a pending round, an
  Always lane and a claim. A separate `--break-claim` would make the intent
  explicit, at the cost of another flag.
- **Matrix and job lanes.** One claim per lane number covers every cell and
  job. Is a per-job claim ever needed?

## Testing

- **Unit (`internal/lane`):**
  - `O_EXCL` races: N goroutines (and N processes, through a helper binary)
    take the same unclaimed lane; exactly one wins and the rest see
    `ErrClaimed` naming the winner.
  - Expiry: a claim past `expires` reads as none; the next take replaces it
    and removes the stale file.
  - Refresh: the holder's refresh moves `expires`; another session's refresh
    is refused; lint's refresh never creates a claim.
  - The flock path doesn't collide with a resource lock named `claims`.
  - Session id: `SWIM_SESSION` wins, then `$USER@host`.
- **e2e (`internal/e2e`):**
  - Two sessions (`SWIM_SESSION=a`, `=b`) contend over `swim new 3`: `a`
    wins, `b` is refused with the holder's name; `swim status` shows the
    claim; `b`'s `swim stub 3` and `swim archive 3` are refused too.
  - `b` runs `swim new 3 --force`: it succeeds, `.swim.log` has one
    `claim-broken` line naming both sessions, and the claim is now `b`'s.
  - An expired claim (lease set to 1s) is taken over by another session's
    `swim new` with no error and no `claim-broken`.
  - `swim claim 3 --break "session died"` logs the reason.
  - `swim run 3` releases the claim; after the run `swim status` shows none.
  - Same `$USER@host`, no `SWIM_SESSION`: the second `swim new` isn't
    refused by the claim (it hits the existing pending-round check instead).

## Out of scope

- **Enforcing claims on file writes.** That needs a filesystem guard or a
  write API; swim doesn't own the planner's editor.
- **Claims across machines, worktrees or the hub.** The hub stays read-only
  (`swimlane.md`, *Out of scope*); cross-host leases need the per-user
  identity in [headless.md](headless.md) first.
- **Changing `Owner:`.** It stays a record of authorship.
- **The queue's pins.** Unchanged.
