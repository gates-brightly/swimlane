# Chain trust anchors: trusted signers and external checkpoints

Status: proposed

## Summary

`swim verify` reports three levels of assurance instead of one. Each level
says what it proves:

| Level | Proves | Today |
|---|---|---|
| **consistent** | seq, prev-hash links, and every signature matches the key **recorded in that entry** | yes (`chain.Check`) |
| **trusted** | every signer is a key pinned **outside the chain and outside the lane's reach** | partly: `--strict` checks signers against `lookup.yml`, which a lane can edit |
| **anchored** | the chain extends a checkpoint (seq, head hash) held somewhere a lane and the runner key can't rewrite | no |

There are two new pieces: **pinned signers** (`.swim/trust` outside the repo,
or a committed `trust.yml` checked against a pin) and **checkpoints** (signed
chain heads exported to a destination you choose, then verified against).

```sh
swim verify                          # consistent, trusted (vs pins), anchored (vs last checkpoint)
swim chain checkpoint                # sign and export the current head now
swim chain export --to s3://audit/swim/orders/   # entries plus checkpoints, append-only
swim verify --against s3://audit/swim/orders/    # anchor to the external copy
```

## Motivation

The audit's Finding 2, confirmed in the code:

- **The verifier trusts the key in the entry.** Each entry carries its own
  `Signer`, and `chain.Check` (`internal/chain/chain.go:238`) checks the
  signature against that key. A chain that was wholesale replaced, with fresh
  entries and a new key, passes `Check`.
- **The pin is editable.** `swim verify --strict` warns on signers not in
  `lookup.yml` (`internal/cli/custody_cmds.go:699`). But `lookup.yml` is in
  the repo, which the lane can write (see `tasks.md` T3).
- **Losing the tail is invisible.** `.swim/chain.jsonl` isn't committed, and
  nothing records the head elsewhere, so dropping the last N entries can't be
  detected. The brief lists this as an open question (*Keeping the chain*).
- **Tests stop short.** Existing tests cover editing, removing, and reordering
  entries (`chain_test.go:27`, `e2e/custody_test.go:102`). None replaces the
  whole chain.

## Behaviour

### Pinned signers

- **Who's trusted:** the runner key (`signing_key`) and approver keys are
  listed in `trust.yml`, which is committed. Its SHA-256 is **pinned** in the
  operator's personal config (`~/.config/swim/`, which a worker can't change;
  see the brief's *Personal settings stay out of reach*) the first time
  `swim verify` sees it, trust-on-first-use. It can also be pinned explicitly:
  `swim keys pin`.
- **Changing it:** a change to `trust.yml` must be signed by a key it already
  trusts (`swim keys trust <key> --sign`). Otherwise verify reports `trusted:
  no (trust.yml changed since pin, unsigned)`.
- **`lookup.yml`** keeps naming people and rules. Its keys must be a subset of
  `trust.yml`. Lint fails otherwise.

### Checkpoints

- **What one is:** `{seq, head_hash, at, host, runner_key_fp}`, SSH-signed
  with the runner key, appended to `.swim/checkpoints.jsonl`, and **sent** to
  each configured `chain.anchors:` destination.
- **Destinations** (any of):
  - `file:` a path outside the repo, e.g. on another volume
  - `s3://` with a bucket that has object lock
  - `syslog:`
  - `hub` the worker's hub, which keeps per-worker checkpoints in its state
    dir
  - `git-notes:` written by the operator, never by swim: the rule against
    writing to git stays
- **When one is taken:** at the end of every top-level `swim run`, `swim
  watch` batch, and `swim ci`, plus on `swim chain checkpoint`. A failed send
  is a warning, not a failed run. It's retried at the next checkpoint, and
  `swim doctor` lists checkpoints that were never sent.
- **What `swim verify` checks:** the newest reachable checkpoint. The local
  chain must contain an entry at `seq` with that `head_hash`.
  - **No such entry:** the result is `anchored: NO — chain truncated or
    replaced after checkpoint seq 412 (2026-10-14T09:12Z, hub)`, with exit
    code 1.
  - **No anchor configured:** the result is `anchored: n/a (no anchors
    configured)`. That's a warning, and an error with `--strict`.

### Export

`swim chain export --to DEST` copies entries past the destination's last seq,
plus checkpoints. It never rewrites what's already there. `swim verify
--against DEST` verifies the local chain as a prefix or extension of the
exported one, so a lost or wiped repo can still be checked.

### Output

Each line in `swim verify` says what it does **not** prove:

```
consistent  yes  418 entries, links and signatures match
trusted     yes  2 signers, all in trust.yml (pinned 2026-09-30)
anchored    yes  extends checkpoint #57 (seq 412) from s3://audit/swim/orders/
note: anchoring proves this history existed by 09:12Z; it doesn't prove the scripts were safe.
```

The YAML form (`swim verify -o yaml`) has `consistent`, `trusted`, and
`anchored` as separate fields, so CI can require any combination.

## Design

| Area | Change |
|---|---|
| `internal/chain` | Add `Checkpoint` type, `Head()`, `CheckAgainst(links, cp)`. Separate `Check` into consistency, and trust given a key set. |
| `internal/chain` `lastLine` | Must handle long last records first. See `tasks.md` T24. Checkpoints call it on every run. |
| `internal/cli/custody_cmds.go` | `swim verify` with three levels and `--against`, plus `swim chain checkpoint\|export`. |
| `internal/lookup` / new `internal/trust` | Load `trust.yml`, check the pin, check signed changes. |
| `internal/config` | `chain.anchors: [...]` (shared), and the pin (personal). |
| `internal/hub` | `PUT/GET /api/workers/{id}/checkpoints`, append-only per worker. The hub refuses a checkpoint whose seq isn't higher than the last one it has. |
| `internal/doctor` | Checks: anchors configured, last checkpoint sent, `trust.yml` pinned. |

## Threat model (goes into the T4 docs)

- **Anchoring detects:** truncation, wholesale replacement, and rollback, in
  each case **after** a checkpoint left the machine.
- **Anchoring can't prove:** anything about entries written after the last
  checkpoint. Someone holding the runner key can write false entries going
  forward.
- **Key compromise:** someone holding the runner key **and** write access to
  every anchor can rewrite everything. That's why anchors should be held by
  someone other than the operator (object lock, hub admin, syslog
  collector).

## Open questions

- Should the default anchor be the hub, when one is enrolled? That's zero
  config, but it makes the hub a security-relevant store.
- Should checkpoints also carry a Merkle root, so a single entry can be proven
  without shipping the whole chain?
- Should `trust.yml` be a separate file, or a `trusted: true` attribute on
  `lookup.yml` keys plus a pin of `lookup.yml`'s key section? A separate file
  is clearer. Folding it in is less to learn.

## Testing

- **Unit, replacement:** a wholesale replacement with a new key and fresh
  entries passes `consistent`, fails `trusted` with pins, and fails
  `anchored` with a checkpoint.
- **Unit, truncation:** truncating to before the checkpoint seq fails
  `anchored`. Truncating after it is reported as `entries since checkpoint:
  N` only.
- **Unit, pins:** a `trust.yml` edit that isn't signed by a trusted key fails
  `trusted`. A signed edit passes.
- **e2e:** a `file:` anchor on another tempdir.
  - Run twice, delete `.swim/chain.jsonl` lines, and expect `swim verify` to
    exit 1 with `anchored: NO`.
  - With `--against`, the same check runs after the repo dir is deleted and
    re-created.
- **Hub:** it rejects a checkpoint seq that goes backwards.

## Out of scope

- A transparency log service, or a third-party timestamping authority (RFC
  3161). Either could become an anchor type later.
- Signing with hardware-backed attestation of the host.
