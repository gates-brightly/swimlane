# Read-only preview: `swim plan --check`

Status: proposed

## Summary

`swim plan --check [N|JOB ...]` runs each selected round's `snapshot` and
`check` stages for real, against live state, and stops every round where
`change` would begin. The plan tree then shows not just what would run, but
whether each round's gates pass today:

```
$ swim plan --check
[+] swim 1  Fetch pages                          check PASS  (4 steps, 3.1s)
├── [+] swim 2  Convert to Markdown                check PASS
│   └── [+] swim 4  Join: build report  wait [2,3]  check ~ (depends on unrun changes in 2, 3)
└── [!] swim 3  Cut over orders-api               stops at gate "DeletionPolicy is Retain"

Plan: 3 to run, 1 would stop at a gate. Guarded changes: swim 3 ORDERS_ALLOW_SST_REMOVE.
```

## Motivation

- **`swim plan` only covers scheduling.** It shows order, waits and skips,
  but not whether a round will get past its gates. Today the only way to find
  out is to run the round, and a gate failing halfway through a batch wastes
  the batch.
- **The stages already say what's safe to run.** By convention, `snapshot`
  and `check` are read-only and every change sits in `change`. A preview that
  runs exactly those stages is a cheap dry run against real state.
- **Approvals need it.** It gives approvers something concrete to review
  ([plan-approvals.md](plan-approvals.md)).

## Behaviour

### What runs

- **The read-only stages run for real:** everything up to the first
  `stage change` runs as usual, including `snapshot`, `run` and `gate`. The
  round ends when it reaches `change`, recorded as `CHECKED`, not `PASS`.
- **No approvals:** guards are never asked and never approved, even with
  `-y` or a flag set. A guard reached before `change` is a dry run, and lint
  already warns about guards outside `change`.
- **Lanes run in parallel,** ignoring `After:` because nothing changes.
  `Locks:` still apply, since two checks may still contend for the same
  resource (for example a Terraform state lock).
- **Dependent lanes are marked `~`:** a lane that waits on another lane's
  changes is checked against today's state, which may differ from the state
  after its dependencies run.
- **Rounds without stages are refused:** with no stage calls there's no safe
  stopping point. The lane shows as `cannot check: no stages` and is not run.

### What it records

- **Its own log:** `.swim/logs/agentN.check.log`, kept apart from the lane's
  round log.
- **Lane status is untouched:** a check never makes a lane "passed", and
  never counts toward `swim all`'s pending selection.
- **Status fields:** `check_result` and `check_at` are added to the lane's
  status. A check older than the script (by SHA) is shown as stale.
- **The chain:** a `check` entry records the script hash, the check-log
  hash and the result.
- **`--yaml`:** `swim.plan/v1` gains a `check` block per lane: result, the
  gate that stopped it, and the guards it would reach.

### Options

- **Common options:** `--parallel N`, `--timeout D` (a cap for the whole
  check), `--yaml`.
- **CI:** `swim ci --check` runs checks only, for a pull request preview.

## Design

- **Library:** the launcher exports `SWIM_CHECK_ONLY=1`. `stage change`
  (and `stage verify` reached without `change`) calls `_swim_checked`,
  which records `CHECKED` and exits 0. `guard` returns "dry run" without
  asking.
- **Wrapped lanes:** `swim stage change` sends the same signal through
  `_wrap`.
- **Launcher:** a `CheckOnly` option. In that mode it uses the check log,
  doesn't update the pass state, and ignores `After:`.
- **Plan rendering (`internal/launcher/plan.go`):** merges check results
  into the tree.
- **Status, logparse, chain:** the new `CHECKED` result and `check`
  entries, all additive.

## Open questions

1. **Trust:** swim can't prove that `snapshot` and `check` are read-only.
   Should lint go further (for example flag `aws * delete-*` or
   `terraform apply` before `change`), or is the stage convention enough?
2. **Respect `After:`?** Optionally (`--ordered`) run checks in dependency
   order, for rounds whose checks read upstream outputs
   ([lane-outputs.md](lane-outputs.md))?
3. **Reuse the result:** should a passing check that's recent enough let the
   real run skip its `check` stage? Probably not: state can change between
   check and run.

## Testing

- **Stopping point:** a round with all four stages stops at `change`. Its
  `snapshot` and `check` steps run, and `change` and `verify` don't.
- **Guards:** never asked, even with `-y` or the flag set.
- **Status:** lane pass state is unchanged, `check_result` is written, and a
  check goes stale when the script changes.
- **Plan output:** a gate failure, `~` for dependent lanes, and a round with
  no stages refused.
- **Locks:** apply. `After:` is ignored.
- **Wrapped lanes:** a Python lane stops at `swim stage change`.

## Out of scope

- **Simulating `change`:** swim doesn't fake mutations or predict their
  effects.
- **Checks on a schedule.** See the queue and watch.
