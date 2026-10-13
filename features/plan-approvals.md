# Approve the plan, not just the script

Status: proposed

## Summary

An approval can cover a round's *evidence* (the output of steps that show
what will change, such as `terraform plan`) as well as its script. Before
`change` runs, swim compares the round's live evidence with the approved
evidence. If they differ, the approval doesn't apply.

```bash
stage check
evidence "tf plan" bash -c 'terraform -chdir=infra/orders plan -no-color | sed -n "/Terraform will perform/,\$p"'
stage change
if guard ORDERS_ALLOW_APPLY "apply orders plan"; then
  run "tf apply" terraform -chdir=infra/orders apply -auto-approve
fi
```

```
$ swim approve 3 --guards ORDERS_ALLOW_APPLY
evidence from check at 12:01 (swim plan --check):
  tf plan:  # aws_dynamodb_table.orders will be destroyed
            Plan: 0 to add, 1 to change, 1 to destroy.
Approve ORDERS_ALLOW_APPLY for lane 3 (script e91a058b, evidence 4c1d77e0)? Y/N [N]
```

**Not in watch mode:** batches run by `swim watch` don't check evidence.
They keep today's behaviour (see [Watch mode](#watch-mode)).

## Motivation

- **Approvers sign bash.** An approval proves someone approved a script, not
  that they knew what it would do to live state.
- **State changes between approval and run.** A `terraform plan` that showed
  "1 to change" at approval time can show "3 to destroy" an hour later. The
  approval should not carry over.
- **Reviews and audits need it:** "they approved this plan" is what reviews
  and audits actually want to see.

## Behaviour

### Evidence steps

- **What `evidence` is:** `evidence [options] "<label>" cmd ...` is a `check`
  stage step, like `snapshot`. Its output is saved and hashed as evidence.
  A failed evidence step fails like a `gate`.
- **Normalizing:** the hash covers the output after ANSI is stripped, line
  endings are normalized and trailing whitespace is removed. Rounds should
  print stable output: cut timestamps and refresh noise (as in the
  example), or hash a machine form such as `terraform show -json`.
- **Gathering it:** evidence comes from [plan-check.md](plan-check.md)
  (`swim plan --check`) or from a real run's `check` stage.

### Approving

- **What `swim approve N --guards G` does:** it shows the latest evidence
  for that exact script and signs both the script SHA and the evidence hash.
  With no evidence, or evidence older than the script, it refuses and says to
  run `swim plan --check N`. `--no-evidence` approves the script only, as
  today.
- **The terminal prompt:** when a guard asks during a run, the prompt shows
  the evidence gathered in this run's `check` stage, so the person answering
  sees what they're approving.
- **`lookup.yml`:** `require` rules may set `evidence: true`, so a guard
  only counts approvals that cover evidence.

### At run time

- **The comparison:** when a guard is reached, swim compares this run's
  evidence hash with the one each signed approval covers.
  - **Match:** the approval counts as usual.
  - **Mismatch:** the approval doesn't count. The log shows `evidence
    changed since approval` and a diff of the evidence. The guard falls
    through to the next approval source: the terminal prompt with the new
    evidence, or a dry run when no one is there to ask.
- **Recorded:** the round's chain entry records the evidence hash, and each
  approval entry records which evidence it covered.

### Watch mode

- **No evidence checks:** batches run by `swim watch` (with or without
  `--approved`) skip evidence comparison entirely. Approvals apply on the
  script hash alone.
- **Rules are ignored:** `evidence: true` rules don't apply to watch runs.
- **Logged:** the log notes `evidence not checked (watch)`.
- **Why:** watch runs are unattended. A plan that drifts by one line would
  turn every queued change into a dry run, with no one there to re-approve.
  Teams that need evidence-bound approval run those rounds at a terminal or
  in CI.

## Design

- **Library:** `evidence` is `snapshot` plus `--evidence`. `swim step`
  writes `.swim/evidence/<job>/<label>.txt` and its normalized hash. The
  round's evidence hash is the hash of the sorted `label:hash` lines.
- **Approvals (`internal/approval`, `internal/sigfile`):** approve events
  gain an optional `evidence_sha256`. Older events still verify and count as
  script-only.
- **Policy (`internal/policy`, `internal/lookup`):** a new `evidence` rule
  key. Approvals are matched on the script SHA and, when required, the
  evidence hash.
- **Guard hook (`cmdApproveHook`, `internal/cli/hooks.go`):** gets the
  run's evidence hash and skips the comparison when `SWIM_QUEUE` is set by
  watch.
- **Diff:** a unified diff of the approved and live evidence goes in the log,
  masked.

## Open questions

1. **Watch mode and `evidence: true`:** should a guard whose rule requires
   evidence refuse to run under watch (a dry run), instead of silently
   applying on the script hash? This is safer, but means such rounds can
   never run unattended.
2. **Normalizing:** is the built-in cleanup enough, or should `evidence`
   take a `--normalize "<cmd>"` filter?
3. **Several evidence steps:** one hash for the round (as above), or
   approval per evidence step?
4. **How long evidence stays valid:** should approvals also expire when the
   evidence is older than some limit, even if it still matches?

## Testing

- **Matching:** identical evidence lets the approval count. Changed evidence
  makes it not count, with a diff, falling through to the prompt or a dry
  run.
- **Normalizing:** ANSI and trailing whitespace differences still match.
- **Approve:** refused with no evidence or stale evidence; `--no-evidence`
  works.
- **Rules:** `evidence: true` ignores script-only approvals at a terminal
  and in CI.
- **Watch:** evidence isn't compared, `evidence not checked (watch)` is
  logged, and script approvals apply.
- **Chain:** approval and round entries carry evidence hashes, and
  `swim verify` passes with mixed old and new entries.

## Out of scope

- **Understanding plans:** swim compares evidence; it doesn't parse
  Terraform or know what "destroy" means.
- **Evidence for steps outside `check`.**
