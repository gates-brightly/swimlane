# Execution manifest: approve and run one exact operation

Status: proposed

## Summary

Every round gets an **execution manifest**, built by the launcher before a
lane starts. It names everything swim can pin about the operation:

- the script bytes, and every file the script includes
- the sidecar
- the runner and its version
- the policy-relevant config
- the env-file hash
- the job identity
- the evidence the approval was based on

Approvals sign the **manifest hash**, not just the script hash. The lane runs
from a **staged copy** of the manifest's files, and the manifest is verified
again right before exec.

Approval prompts are generated from the manifest and the launcher's own state,
not from what the lane's request says.

Things swim can't pin, such as remote state, `PATH` resolution, and package
downloads, are listed as **observed** facts: recorded at approval time and
checked again before the guarded step, not claimed to be frozen.

```yaml
# .swim/manifests/r-20261014-0912-3.yml (also referenced from the chain)
schema: swim.manifest/v1
job: j-orders-drop
lane: 3
run: r-20261014-0912
pinned:
  script:   { path: lane.3.sh, sha256: 9c1e... }
  includes: [ { path: lib/orders.sh, sha256: 41ab... } ]
  sidecar:  { path: lane.3.yml, sha256: 77d0... }
  runner:   { name: bash, path: /opt/homebrew/bin/bash, version: "5.2.37", sha256: e3b0... }
  config:   { sha256: 5f2a..., keys: [blocked, runners, secrets, approvals, lookup_rules] }
  env:      { sha256: 0d4c... }
  evidence: { sha256: a8f1... }        # the plan the approver saw, when evidence-bound
observed:                               # checked again before guarded steps; not frozen
  path: { terraform: /usr/local/bin/terraform@sha256:..., aws: /usr/local/bin/aws@sha256:... }
  identity: { aws_account: "123456789012" }   # from a declared `Observe:` probe
manifest_sha256: 3be9...
```

## Motivation

- **The audit's Finding 4.** A script hash identifies a script's bytes, not
  the operation. Today `lane.ScriptBytes` (`internal/lane/matrix.go:350`)
  leaves out:
  - files pulled in with `source` that aren't listed in `Includes:`
  - the sidecar
  - the runner
  - `swim.yml` policy
  - for non-queued runs, env files
- **Time of check vs. time of use** (`tasks.md` T1). The runner execs the
  working-tree path (`internal/runner/runner.go:132`), and bash reads the
  script while it runs. The copy that `keepScript` makes
  (`internal/cli/custody.go:52`) is for audit only. The queue's SHA pin is
  checked before the slot wait (`internal/launcher/launcher.go:620`), not
  right before exec.
- **The audit's Finding 3: where requests come from.** An approval request
  carries a lane, job, flag, and description supplied by the caller
  (`internal/approval/approval.go:27`). The operator sees that text. Nothing
  binds the question shown to the operation that will actually run.

## Behaviour

### Building and staging

1. **Before** taking locks, the launcher resolves the lane's manifest:
   - parse the script and its sidecar;
   - expand `Includes:`;
   - find **undeclared `source`/`.` lines** by a static scan. An
     undeclared local source is an error (`swim lint` already sees most of
     them), and a dynamic `source "$x"` is a lint error unless declared;
   - resolve the runner;
   - hash the policy subset of the config.
2. It copies every pinned file into `.swim/runs/<run>/<lane>/` (read-only,
   mode 0400), with the same relative layout, and records the manifest.
3. **Immediately before exec** (after the slot wait), it re-hashes the
   **staged** files and compares them with the manifest, and compares any
   pins: the queue's script SHA and env SHA, and the signed approval's
   manifest hash. A mismatch refuses the lane: `blocked: manifest changed
   since queued (includes lib/orders.sh)`.
4. The lane execs **the staged copy**. Its working directory stays the repo
   root, so relative paths to data files behave as today. `Includes:` paths
   are rewritten through `SWIM_STAGE`, and `lane_init` in `lib.sh` points
   `source` lookups there.

### Approvals bind to the manifest

- **What's signed:** `swim approve N` signs `manifest_sha256` and lists the
  manifest's pinned entries in the approval file, so a reviewer sees what they
  approved.
- **Compatibility:** an approval that only names a script SHA still counts
  while the other pinned inputs are unchanged since signing. Its sigfile
  records the manifest hash at the time, so it can be checked.
- **Policy drift:** changing `swim.yml`'s policy subset or `lookup.yml` rules
  invalidates manifest-bound approvals. That answers part of `tasks.md` T14.
  `swim approve --list` says why: `stale: config policy changed (blocked
  patterns)`.

### Questions are generated, not supplied

- **Where the approval broker gets its facts:** the launcher already knows
  each lane's run, job, and manifest. The broker identifies the requesting
  lane by **its per-lane socket and token** (`tasks.md` T3/T10), not by the
  request's `lane` field. It uses `lane` and `job` from its own state.
- **Flag:** the requested flag must be among the guards **declared in the
  manifest's script** (the `Guards` header and the `guard` calls lint finds).
  An undeclared flag is refused and logged.
- **What the prompt shows:**
  - **Authoritative:** lane, job, flag, manifest hash, approvers needed, and
    evidence hash. This is in bold.
  - **The lane's description:** labelled as such ("the lane says: ...") and
    masked.
- **One-time responses:** each response carries a request id, and the
  launcher records it as consumed by that lane, run, and flag. A second
  `_mark APPROVED` for the same id, or one from another lane, is refused.

### Observed facts

- **Declaring them:** `Observe:` header lines declare probes. Probes must be
  side-effect free (see the brief's *Safety* section).

  ```
  # Observe: aws_account = aws sts get-caller-identity --query Account --output text
  # Observe: path terraform aws
  ```

- **When they're checked:** at approval time the result is stored in the
  manifest's `observed` block. Immediately before each guarded step, swim
  runs the probes again. A difference ends the round as **drift**, using the
  existing drift path: `drift: aws_account was 123456789012 at approval,
  now 210987654321`.
- **What they don't do:** they don't freeze anything. The docs say they
  narrow the gap between approval and use; they don't close it.

## Design

| Area | Change |
|---|---|
| `internal/lane` | `Manifest` type; `BuildManifest(root, n, cfg)`, which grows from `ScriptBytes`; static scan for `source`. |
| `internal/launcher` | Build before locks, stage, then verify again after the slot wait, immediately before `cmd.Start` (`launcher.go:950`). Pass `SWIM_STAGE` and `SWIM_MANIFEST`. |
| `internal/runner` | Exec the staged path, and report the runner's path, version, and hash for the manifest. |
| `internal/config` | `PolicyHash()` over a fixed list of keys. The list is versioned, so adding a key is deliberate. |
| `internal/sigfile` | Approvals carry `manifest_sha256` and the pinned entries. Stale reasons. |
| `internal/approval` | Server keyed by lane socket and token. `Request.Lane` and `Job` are ignored (kept on the wire for older lanes). Request ids, and recording each id as consumed. |
| `internal/cli/approve.go` | `_approve` reads `ScriptSHA` from `SWIM_MANIFEST` (launcher-owned, read-only), not `status.yml`. |
| `internal/chain` | Start-of-round entries record `manifest_sha256`. `swim verify` checks staged copies against it. |
| `internal/lint` | Undeclared and dynamic `source`, `Observe:` probe syntax, and a warning for side effects in a probe (using the same blocked-command check as gates). |
| `internal/queue` | Entries pin `manifest_sha256`. Today's `script_sha256` and `env_sha256` are kept as a fallback for older entries. |

## Open questions

- **Should the runner's binary hash be pinned or observed?** An OS update
  that changes bash would invalidate every approval. Proposal: observed, and
  the version is pinned only when the sidecar says `runner_pin: true`.
- **Python, TypeScript, and Go lanes:** `Includes:` covers local modules.
  Should a lockfile (`requirements.txt` hash, `package-lock.json`) be pinned
  automatically when it sits next to the lane?
- **Matrix rounds:** one manifest per cell, or one for the round with a
  hash for each cell's values?
- **Garbage collection:** how long to keep `.swim/runs/<run>/`. Proposal: as
  long as the round's log, archived together.

## Testing

- **Time of check vs. time of use (e2e):** a lane waits on a lock. Edit
  `lane.3.sh` and an included file, then release the lock. The lane is
  refused, or (when it's not pinned) runs the original staged bytes. Separately,
  edit the working-tree script mid-run and check that bash keeps executing the
  staged copy.
- **Coverage table (unit):** changing each pinned input changes
  `manifest_sha256`, and changing a non-policy config key doesn't.
- **Approval binding (e2e):** sign, change the included file, and the guard is
  not approved, with the stale reason shown. Change only `swim.yml`'s policy
  subset and get the same result.
- **Where requests come from (e2e):** a lane sends a request naming another
  lane, another job, and an undeclared flag with a misleading description:
  - the prompt shows the true lane and job;
  - the undeclared flag is refused;
  - the description is shown labelled as the lane's;
  - the chain attributes the request to the true lane.
- **Response reuse (e2e):** an approval response replayed through `_mark` by a
  second lane is refused.
- **Observed drift (e2e):** a probe whose output changes between approval and
  the guarded step ends the round as drift.

## Out of scope

- Hermetic execution (containers, Nix). The manifest records what ran; it
  doesn't build an environment.
- Freezing remote state. Observed probes only detect change.
