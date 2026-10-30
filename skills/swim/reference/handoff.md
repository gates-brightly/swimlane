# Handing a round to the operator

The operator reads your message, not your reasoning. Give them a command
they can paste, what it will do, and what each approval allows. Then stop
and wait for them to run it.

How they approve is theirs. The command you give runs the round and nothing
more: no guard or decision variables, no approval flags, and no advice on
how to approve. Describe what each guard does, not how to say yes to it.

## What you're approving

Every hand-off with a guard or a decision has this block, one entry per
guard or decision. Write it from the script and from what the snapshot and
check stages found, not from memory:

````markdown
**What you're approving**

- **`USERS_ALLOW_PURGE`** (swim 1, change stage)
  - Runs: `fakecloud delete users @test.example`
  - Target: the prod `users` table (account 123456789012)
  - Scope: 3 rows (`load-1..3@test.example`); the 3 real users stay
  - Undo: the `users before` snapshot has every row; `users-restore.sh` puts them back
  - If you say no: a dry run; nothing is deleted, verify fails, swim run exits 3
- **Decision `ROLLOUT`** (swim 2): 1 canary 10% then the rest (~20 min),
  2 all at once (~2 min, no canary), 3 don't deploy (the round ends there)
````

The guard's own description (what swim shows the operator) says the same in one
line; this block is the detail behind it. Mark anything that can't be
undone, or touches production or shared state, with ⚠.

## Run it now

````markdown
Ready to run: **swim 2** (cut over orders-api), after swim 1 passes.

```sh
swim run 3f2a9c1e 7b4d4e2a        # pinned to the jobs I wrote: a rewritten lane is refused
```

- Read-only until the change stage. It snapshots the CFN template, checks
  the account is orders-prod (123456789012), and gates on DeletionPolicy:
  Retain.
- Verify re-runs `terraform plan` and expects no changes.

**What you're approving**

- ⚠ **`ORDERS_ALLOW_SST_REMOVE`** (swim 2, change stage)
  - Runs: `npx sst remove --stage prod`
  - Target: SST stack `orders-prod` in account 123456789012
  - Scope: the 14 CloudFormation resources the snapshot lists; terraform
    already owns them, and DeletionPolicy: Retain (gated) keeps the data
  - Undo: `npx sst deploy --stage prod` from `v2.3.9`
  - If you say no: a dry run; nothing is removed.

When it's done, say so (or don't: I'll read `swim report` next time).
````

## Queue it for unattended running

A swimmer (`swim watch`) runs a queued batch once its lanes are free. Any
guard the operator hasn't approved is a dry run, unless they approve it on
`swim watch`'s screen when the lane gets there (it asks, in yellow, naming
the lane). When you queue it (you may; you never approve), say what you
queued and flag the risk:

````markdown
Queued #41: swim 22 and 23.

⚠ swim 22, `L22_ALLOW_ZG_SMOKE`: applies tf/smoke under zg in sbx1, reads it
  back, then destroys it (the zg-tf-smoke-* role, security group and
  parameter). Unapproved, it dry-runs.
````

If a script changes after it's queued, that lane is skipped, not run.

## Anything you need from the operator first

- Config or `deps:` changes: give the exact YAML lines, and they apply them.
- A secret: they set it in their own environment (or `.env.swim`), and the
  lane reads the variable. Never ask them to paste a value to you.
- Something only they can see: a read-only command to run and send back,
  or better, a `snapshot` step in the next round.
