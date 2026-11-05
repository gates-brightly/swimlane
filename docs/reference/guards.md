# Guards

```bash
if guard ORDERS_ALLOW_SST_REMOVE "remove SST stack orders-prod (2026-10-09: tf owns it)"; then
  run "sst remove" npx sst remove --stage prod
fi
```

The action string is what the person approves, often all they read: one
line with the action, the exact target, the scope (how many, from what a
check counted), whether it can be undone, the date and the reason. "delete
the 3 @test.example rows from prod users (snapshot keeps them) 2026-10-29:
load-test cleanup", not "cleanup". Can't say it in a line? Split the guard.
swim strips escape sequences, control characters and bidi overrides from
it.

## How a guard is decided

In this order:

1. **The flag:** `FLAG=1` in the environment `swim run` started with
   approves; `FLAG=0` declines without asking. A flag a lane sets for
   itself doesn't count. Env files can't hold one.
2. **A queued batch's approvals:** `swim queue N --approve FLAG` (or
   `-y`), for that batch, pinned to its exact [manifest](manifest.md).
3. **The person at the terminal:** `swim 2 requires human approval of guard
   FLAG ... the lane says: <action>  Y/N [N]`. Or in the live view: `y`/`n`
   on a lane that's asking, or ahead of time on one that hasn't (this run
   only).
4. **`-y`** (`swim all -y`, `swim watch -y`): approves every question; the
   log, `swim status`, the chain and `swim ci`'s summary say so.

Anything else is a **dry run**: the step is skipped, the log says how to
approve, and the round ends **SKIP**, not PASS: lanes after it don't start,
`swim run` exits 3 (`swim ci`: 0), `swim all` runs it again next time.

Under `swim run` the launcher decides, not the lane: it reads its own
environment once as the run starts, refuses a flag the header doesn't
declare, lets each approval be used once, and writes every approval to the
chain itself. A lane can't approve itself. `approvals: flags` in config
never asks.
