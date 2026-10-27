# Handing a round to the operator

The operator reads your message, not your reasoning. Give them a command
they can paste, what it will do, and what each approval allows. Then stop
and wait for them to run it.

## Run it now

````markdown
Ready to run: **swim 2** (cut over orders-api), after swim 1 passes.

```sh
swim run 3f2a9c1e 7b4d4e2a        # pinned to the jobs I wrote: a rewritten lane is refused
```

- Read-only until the change stage. It snapshots the CFN template, checks
  the account is orders-prod (123456789012), and gates on DeletionPolicy:
  Retain.
- **Guard `ORDERS_ALLOW_SST_REMOVE`** approves `npx sst remove --stage prod`
  for orders-prod. swim asks at the prompt. Without a yes, the step is a
  dry run and nothing is removed. To approve up front:
  `ORDERS_ALLOW_SST_REMOVE=1 swim run 3f2a9c1e 7b4d4e2a`.
- Verify re-runs `terraform plan` and expects no changes.
- Rollback if verify fails: `npx sst deploy --stage prod` from `v2.3.9`
  (noted in the script).

When it's done, say so (or don't: I'll read `swim report` next time).
````

## Queue it for unattended running

Queue only what you'd hand over to run with nobody watching. A queued batch
never asks, so any guard it doesn't approve is a dry run.

````markdown
For tonight's window, queue it with the approval up front:

```sh
swim queue add 4 5 --approve SESSIONS_ALLOW_PURGE --note "2.4.1 rollout + session cleanup"
# then, when the window opens (by hand, or cron): swim watch --once
```

`SESSIONS_ALLOW_PURGE` approves deleting sessions marked stale (about 3,100
rows; the snapshot keeps them). A swimmer (`swim watch`) runs it once lanes 4
and 5 are free. If either script changes after you queue it, that lane is
skipped, not run.
````

When you queue it yourself (you may; you never approve), say what you
queued and flag the risk:

````markdown
Queued #41: swim 22 and 23, no approvals.

⚠ swim 22, `L22_ALLOW_ZG_SMOKE`: applies tf/smoke under zg in sbx1, reads it
  back, then destroys it (the zg-tf-smoke-* role, security group and
  parameter).
⚠ A `swim watch -y` swimmer is up: it approves every guard, so the step above
  runs. Without that swimmer it would dry-run; to approve it yourself:
  `swim queue add 63bdf4a9 0c9210bf --approve L22_ALLOW_ZG_SMOKE`.
````

## Needs signed approvals

When `lookup.yml` requires approvals from a group, name who needs to sign
and give them the command. Don't run `swim approve` yourself.

````markdown
`FIN_ALLOW_DELETE` needs two people from `dba` (lookup.yml). Each runs, from
their own checkout:

```sh
swim approve 1 --guards FIN_ALLOW_DELETE
```

…and commits the file it writes in `approvals/`. Once two have signed, it
runs unattended (`swim watch --approved`, or the operator's `swim run 1`).
An edit to lane.1.sh after they sign makes their approvals stale.
````

## Anything you need from the operator first

- Config or `deps:` changes: give the exact YAML lines, and they apply them.
- A secret: they set it in their own environment (or `.env.swim`), and the
  lane reads the variable. Never ask them to paste a value to you.
- Something only they can see: a read-only command to run and send back,
  or better, a `snapshot` step in the next round.
