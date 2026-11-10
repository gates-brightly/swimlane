# Guards

```bash
# Guards: ORDERS_ALLOW_SST_REMOVE  remove SST stack orders-prod (14 resources, data retained)
#   why: terraform owns orders-prod since 2026-10-09
...
if guard ORDERS_ALLOW_SST_REMOVE; then
  step "sst remove" npx sst remove --stage prod
fi
```

A guard says two things in its header, and the operator reads both
before answering, often nothing else:

- **What it does**, on its `# Guards:` line: the action, the exact
  target and the scope (how many, from what a check counted), in one line
  of at most 100 characters. "delete the 3 @test.example rows from prod
  users (snapshot keeps them)", not "cleanup". Can't say it in a line?
  Split the guard.
- **Why**, on the `#   why:` line right under it: the reason for the
  change, with its date. "load-test cleanup (2026-10-29)". `why:` is
  lowercase on purpose: indented, it belongs to the guard above it, not
  a header key of its own like `Guards:`.

`guard FLAG` takes both from the header, from the lane's pinned copy
under `swim run`: a lane can't change at run time what you're asked. The
question shows them as the lane's words:

```text
lane 2 requires human approval of guard ORDERS_ALLOW_SST_REMOVE
  round-id 3f2a9c1e-... · manifest 3be9a1c07d2e
  the lane says:
    does: remove SST stack orders-prod (14 resources, data retained)
    why:  terraform owns orders-prod since 2026-10-09
(or set ORDERS_ALLOW_SST_REMOVE=1 to approve without asking)  Y/N [N]
```

`swim status`, `swim watch` and its notifications show the same two
lines. `swim lint` warns on a guard with no why (`guard-why`), one whose
line says too little or too much (`guard-does`), and a guard call that
gives its own text (`guard-text`; `guard FLAG "<text>"`, the older form,
still runs until the first release on or after 2026-12-01, and `swim
lint --fix` drops text that only repeats the header). swim strips escape sequences, control characters and bidi
overrides from both.

## How a guard is decided

In this order:

1. **The flag:** `FLAG=1` in the environment `swim run` started with
   approves; `FLAG=0` declines without asking. A flag a lane sets for
   itself doesn't count. Env files can't hold one.
2. **A queued batch's approvals:** `swim queue N --approve FLAG` (each
   flag), for that batch, pinned to its exact [manifest](manifest.md); or
   `y` on `swim watch`'s screen when the batch asks.
3. **The person at the terminal:** `lane 2 requires human approval of guard
   FLAG ... the lane says: <action>  Y/N [N]`. Or in the live view: `y`/`n`
   on a lane that's asking, or ahead of time on one that hasn't (this run
   only).

Those are the only ways: there is no "approve everything" switch.

Anything else is a **dry run**: the step is skipped, the log says how to
approve, and the round ends **SKIP**, not PASS: lanes after it don't start,
`swim run` exits 3 (`swim ci`: 0), `swim all` runs it again next time.

Under `swim run` the launcher decides, not the lane: it reads its own
environment once as the run starts, refuses a flag the header doesn't
declare, lets each approval be used once, and writes every approval to the
chain itself. A lane can't approve itself. `--no-prompt` answers every
question no (and CI, `--yaml` or no terminal never ask): only `FLAG=1`
approves.
