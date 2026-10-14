# Guarded secrets: approval enforced by credentials

Status: proposed

## Summary

A secret in swim's vault can be marked as **guarded by a flag**. The run's
secret broker hands it to a lane only after that lane's `guard FLAG` was
approved in this run. The approval must come from the launcher's own broker,
from signed approvals, or from the queue entry. Today a script can skip
`guard` and call the cloud CLI directly. With this feature, a script that
skips `guard` never receives the credential that makes the destructive call
possible. This turns guards from a convention that cooperative scripts follow
into an enforced boundary, **for credentials that live only in swim's vault**.

```yaml
# swim.yml
secrets:
  PROD_DB_ADMIN_URL: { guard: ORDERS_ALLOW_DROP }    # released only after that guard is approved
  AWS_PROD_WRITE_ROLE: { guard: [INFRA_APPLY, INFRA_DESTROY] }  # either one
```

```bash
guard ORDERS_ALLOW_DROP "drop the orders table" || exit 0
run "drop table" psql "$(swim secret get PROD_DB_ADMIN_URL)" -c 'drop table orders'
```

## Motivation

- **The audit's Finding 1:** a guard is a bash function
  (`internal/assets/lib.sh:402`). A lane can skip it, set `FLAG=1` itself, or
  call the CLI directly. Today the guarantee only holds for cooperative scripts
  (see `tasks.md` T4).
- **An executable sandbox is the wrong tool.** Sandboxing arbitrary bash, or
  shimming every cloud CLI the way the git shim works, is brittle. A script
  can reach the same API through curl, an SDK, or another binary.
- **Credentials are where authority actually lives.** swim already has a
  per-run secret broker (`internal/secrets`) that decrypts the vault once and
  serves lanes over a token-checked socket. The approval broker
  (`internal/approval`) runs in the same launcher process. Connecting the two
  makes approval a precondition for authority, not a request to be polite.

## Behaviour

- **Declaring a guarded secret:** add `guard: FLAG | [FLAGS]` to the secret in
  `swim.yml`. That is shared config: committed, reviewed, and outside the
  lane's reach once T3 snapshots config at round start.
- **When the broker releases it:** `swim secret get NAME` (and `_swim.<name>`
  steps) returns the value only if the launcher has recorded an **approved**
  decision for one of those flags, for this lane, this run, and this job.
  Otherwise the call fails with `refused: NAME is guarded by
  ORDERS_ALLOW_DROP, which isn't approved in this run`. The refusal is written
  to the lane log and the chain.
- **Approvals that count:**
  - the prompt (terminal, live view, or the hub per `features/headless.md`)
  - signed approvals (`approvals/*.signature`) that meet the `lookup.yml` rule
  - the queue entry's `approve:` list
  - `swim run -y`
- **Approvals that don't count:** `FLAG=1` in the lane's own environment. The
  launcher only recognizes flags it set itself (`-y`, queue, CLI flags), not
  values a script exports.
- **Dry runs:** a declined or dry-run guard never releases the secret, so the
  dry-run path can't accidentally act.
- **Lifetime:** the release lasts until the lane exits. With `release: step`,
  it is tied to the next step only: the broker hands out the value once and
  refuses a second `get`.
- **Bypassing it:** `--bypass-secret-guards` isn't offered. If an operator
  wants the secret without a guard, they edit `swim.yml`, and that edit is
  reviewed.
- **Lint:** `swim lint` warns when a lane uses a guarded secret with no
  matching `guard` call on the path to it. It also warns when it sees the
  matching raw credential (a variable such as `AWS_PROFILE=prod-write`, or a
  `~/.aws` profile name listed in `swim.yml` `ambient_credentials:`) used
  directly.

## Design

| Area | Change |
|---|---|
| `internal/config` | Secrets take a `guard:` field (string or list) and `release: lane\|step`. |
| `internal/launcher` | Keep an in-memory `decisions[run][lane][flag] = Response`, filled by the approval asker and by flags the launcher itself set. Pass it to the secret broker as a `func(lane int, job, flag string) (approved bool, via string)`. |
| `internal/secrets` | `Server` takes that decision function. `get` checks a secret's guards before returning it, and refusals call `used` with `how: refused`. |
| `internal/cli/approve.go` | `_approve` reports each decision to the launcher, not only to the lane. This depends on the T3 per-run token: otherwise a lane could report a fake decision. |
| `internal/chain` | `secret-released` and `secret-refused` entries: name, flag, via, lane, job. Never the value. |
| `internal/lint` | The two warnings above. |
| `internal/doctor` | Lists guarded secrets. Warns about ambient credentials that grant the same access: `~/.aws/credentials` profiles named in `swim.yml` `ambient_credentials:`. |
| Hub grants | Secrets granted through the hub (`internal/hub/state.go`) can carry the same `guard:`, set by the admin when granting. |

## Limits (state these in the trust-model docs, T4)

- **It only protects credentials that exist nowhere else.** If the operator's
  shell has `~/.aws` admin credentials, the lane inherits them, and this
  feature does nothing for them.
- **Ambient credentials are out of swim's reach.** Recommend running swim with
  read-only ambient credentials and putting write roles in the vault. One way:
  an `sts assume-role` step that uses a guarded secret, so the elevated
  session exists only after approval.
- **Released means usable.** Once released, the lane can use the secret for
  anything. With `release: step`, the next step is the only one that receives
  it through swim, but the lane can still read and reuse the value.

## Open questions

- Should `swim.yml` be able to make a guarded vault the **only** source of
  write credentials? For example, `strip_env: [AWS_*]` would remove ambient
  credential variables from lane environments.
- Should a released secret be revoked (for example, the STS session ended) when
  the step finishes? That requires knowing how each provider revokes, so it
  probably belongs as a recipe, not a built-in.
- Should SDK lanes (`swim.lane.v1`) get a `GetSecret` RPC with the same check,
  or keep going through `swim secret get`?

## Testing

- **Unit:** the broker refuses a guarded secret with no decision, releases it
  after an approved decision for the same lane, run, and job, and refuses it
  for a different lane, job, or run.
- **e2e:** the release succeeds only when it should.
  - **Refused:**
    - the lane skips `guard` and calls `swim secret get`
    - the lane sets `FLAG=1` itself and calls `swim secret get`
    - the guard was declined
  - **Released:**
    - the guard is approved at the prompt
    - the guard is approved by a signed approval
    - the guard is approved by the queue entry
- **e2e:** the chain records each release and refusal, and the value never
  appears in any log (this adds to the leak test, T9).
- **e2e:** `release: step` refuses a second `get` in the same lane.

## Out of scope

- Sandboxing lane processes (namespaces, seccomp, containers).
- Brokering cloud credentials natively (minting STS sessions inside swim).
  Lanes do this with a guarded secret.
