# Other languages

A lane can be any runnable: `lane.N.py`, `.js`, `.ts`, `.rb`, a compiled
`lane.N`. One per lane. There is no SDK: steps are swim commands.

## How it runs

1. `lane.N.sh`: bash.
2. A `#!` line: that command.
3. `runners:` for its extension: yours (config.yml) over the repo's
   `swim.yml`.
4. An executable with no runner: itself.
5. Otherwise swim finds the usual runners on `PATH` (`.ts`: tsx, ts-node,
   bun, deno; `.py`: python3; ...), asks once, and saves the answer as
   `runners.ts` for this repo. With no one to ask (CI, `swim watch`), the
   lane fails at start with the fix: a `#!` line, `swim config runners.ts
   tsx`, or `runners: {ts: tsx}` in `swim.yml`.

## Steps

The program runs inside swim's wrapper, which opens and closes the round
and exports `SWIM_LANE`, `SWIM_JOB`, `SWIM_RUN`, `STEP_LOG` and the rest:

```python
#!/usr/bin/env python3
# Round:  Rotate the orders bucket
# Guards: ORDERS_ALLOW_DELETE  delete the old bucket (2026-10-13: replaced)
import os, subprocess
def swim(*a): return subprocess.run([os.environ.get("SWIM_BIN", "swim"), *a]).returncode

swim("stage", "check")
swim("gate", "new bucket exists", "--", "aws", "s3api", "head-bucket", "--bucket", "orders-v2")
swim("stage", "change")
if swim("guard", "ORDERS_ALLOW_DELETE", "delete bucket orders (2026-10-13: replaced)") == 0:
    swim("run", "delete old bucket", "--", "aws", "s3", "rb", "s3://orders")
```

| | |
|---|---|
| `swim run\|gate\|snapshot [opts] "<label>" -- cmd` | Steps, with the [step options](lanes.md#functions). |
| `swim guard FLAG "<action>"` | Exit 0 approved, 1 a dry run. |
| `swim stage NAME`, `swim drift`, `swim stop`, `swim msg`, `swim input`, `swim output` | As in bash. |

- A failed gate, `swim stop`, a blocked command or Ctrl-C ends the program
  even if it ignores the exit code (SIGTERM to its group, SIGKILL after 5s).
- A program that never calls `swim run` is logged as one step: its whole
  output (masked), its exit code.
- `# Includes: lib/*.py` pins the local modules it imports; a lane runs
  from its staged copy, so an unlisted one isn't found.

## Header

`#` or `//` comments, as in bash. Or a sidecar `lane.N.yml`, whose keys win:

```yaml
round: cut over orders
job: 3f2a9c1e-...
after: [1, 2]
guards:
  ORDERS_ALLOW_DELETE: delete bucket orders (2026-10-13: replaced)
locks: [orders-db]
timeout: 30m
step-timeout: 5m
```
