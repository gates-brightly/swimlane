# Python lanes

A Python lane is a file named `lane.N.py`. swim runs it with its `#!` line
(or `runners: {py: python3}` in `swim.yml`) inside a round, so the program
gets swim's steps, guards, logging and chain without loading the bash
library. There are two ways to talk to swim from Python:

- **The SDK** (`pip install swim-lanes`, `import swim`): steps are method
  calls, checks can run in-process, and handlers are decorators.
- **The CLI**: call `swim run "<label>" -- cmd` with `subprocess`. It needs
  no dependency.

[← back to the README](../../README.md#lanes-in-other-languages) ·
[Node](node.md) · [Go](go.md)

## With the SDK

This lane archives stale rows from the tables listed in `tables.txt`. It
snapshots first, checks the list in Python, gates on the result and
changes only behind a guard. It leaves a report for lanes that depend on
it.

```python
#!/usr/bin/env python3
# swim: syntax 3
# Round:   Archive stale rows from the tables
# Guards:  ALLOW_ARCHIVE  move rows older than 90 days to archive/
# Outputs: report.txt
import json
import os
import swim

with swim.round() as r:
    @r.on_failure
    def tidy(outcome):
        if outcome["failed_stage"] in ("snapshot", "check"):
            return  # nothing was changed yet
        r.run("remove partial archive", ["rm", "-rf", "archive"])

    r.stage("snapshot")
    r.snapshot("tables before", ["cat", "tables.txt"])

    r.stage("check")
    r.gate("tables are listed", ["test", "-s", "tables.txt"])
    tables = open("tables.txt").read().split()
    # The promised output (Outputs:), for lanes that run after this one.
    with open(os.path.join(os.environ["SWIM_OUT"], "report.txt"), "w") as f:
        json.dump({"tables": tables}, f)

    with r.step("every table has a name", gate=True) as s:   # runs here, in Python
        for t in tables:
            s.log(f"table: {t}")
        s.require(all(t.isidentifier() for t in tables), "a table name isn't valid")

    r.stage("change")
    if r.guard("ALLOW_ARCHIVE", "move rows older than 90 days to archive/"):
        for i, t in enumerate(tables, 1):
            r.msg(f"archiving {t}", progress=(i, len(tables)))
            r.run(f"archive {t}", ["mkdir", "-p", f"archive/{t}"])

        r.stage("verify")
        r.gate("every table is archived", ["test", "-d", "archive/users"])
```

Save it as `lane.1.py`, make it executable, and run it:

```sh
chmod +x lane.1.py
swim run 1                    # a dry run: the guard skips the change
ALLOW_ARCHIVE=1 swim run 1    # or approve when the live view asks
```

The approved round's log, from `swim log 1`:

```
-- stage check  10:55:47
  PASS  tables are listed                       0.0s  10:55:47
        $ test -s tables.txt
  PASS  every table has a name (in-process)     0.0s  10:55:47
        $ (in-process)
        | table: orders
        | table: billing
        | table: users

-- stage change  10:55:47
  APPROVED  move rows older than 90 days to archive/ (ALLOW_ARCHIVE=1)
  MSG  archiving orders (1/3, 10:55:47)
  PASS  archive orders                          0.0s  10:55:47
        $ mkdir -p archive/orders
  ...

== END PASS  pass=7 fail=0 skip=0 drift=0  exit=0  0.9s  2026-10-14T10:55:47Z
   stages: snapshot PASS | check PASS | change PASS | verify PASS
   outputs: report.txt 42 B fa0bbffd659a
```

If a table name is bad, the in-process gate fails, the round stops, and
`tidy` returns without touching anything:

```
  FAIL  every table has a name (in-process, a table name isn't valid)
        | table: orders
        | table: bad-name
  STOP  gate failed: every table has a name
```

### What each call does

| Call | What it does |
| --- | --- |
| `with swim.round() as r:` | Opens the round. When it ends, the block runs the handlers and exits with the round's code. |
| `r.stage(name)` | Starts a stage: `snapshot`, `check`, `change` or `verify`. |
| `r.run(label, argv)` | A step that swim runs and logs. Returns `True` if it passed; a failure doesn't stop the round. |
| `r.gate(label, argv)` | A step whose failure ends the round. |
| `r.snapshot(label, argv)` / `r.evidence(label, argv)` | A read-only step whose output is kept, or whose output an approval covers (the plan). |
| `with r.step(label, gate=False) as s:` | An in-process step. `s.log(text)` adds output lines (masked), and `s.require(ok, msg)` fails it. Any other exception fails it too and carries on up. |
| `r.guard(FLAG, action)` | `True` if the operator approves; `False` is a dry run. |
| `r.confirm(question)` | `True` on yes; no ends the round. |
| `r.msg(text, progress=(n, m))` | The lane's status message, in the live view and `swim status`. |
| `r.input(lane, name)` / `r.output(key, value)` | Read a dependency's output (a path, or a small value), or set one of your own. |
| `r.drift(what)` / `r.stop(reason)` | Record drift, or stop the round. |
| `@r.on_success` / `@r.on_failure` / `@r.on_exit` | Handlers that get `outcome` (`outcome`, `stop_reason`, `failed_step`, `failed_stage`). Register them before the first step. |

All of the step calls take `timeout="5m"`, `retry=2`, `backoff="1s"`,
`retry_on=[75]` and `soft=True`.

### Things to know

- **Let `RoundEnded` go.** A failed gate, `r.stop`, an operator stop or a
  time limit raises `swim.RoundEnded`. It's a `BaseException`, so a plain
  `except Exception` doesn't swallow it. Let it unwind your `finally`
  blocks; you get 10 seconds before swim stops the program.
- **`soft=True` means "don't end a `Fail: hard` round".** It doesn't stop a
  failed step from counting. For a snapshot of something that may not
  exist yet, make the command itself succeed.
- **Local modules.** List helpers under `# Includes: lib/*.py` so they're
  hashed with the script: an approval then covers them too.
- **Outputs are read-only.** A dependent lane gets the file's path from
  `r.input(1, "report.txt")`. To publish it, copy it with a fresh mode
  (`install -m 0644`) rather than `cp`.

## Without the SDK

Any program can call the swim CLI. Each call is one step, and its exit
code is the step's result:

```python
#!/usr/bin/env python3
# Round:  Tidy old snapshots
# Guards: ALLOW_TIDY  delete snapshots older than a week
import subprocess

def swim(*args):
    return subprocess.run(["swim", *args]).returncode == 0

swim("stage", "check")
swim("gate", "snapshots exist", "--", "test", "-d", ".swim/snapshots")
swim("stage", "change")
if swim("guard", "ALLOW_TIDY", "delete snapshots older than a week"):
    swim("run", "delete old snapshots", "--", "find", ".swim/snapshots", "-mtime", "+7", "-delete")
```

A failed `gate` ends the program for you, even if it ignores the exit
code. Handlers for CLI lanes are commands in the header, with
`# swim: syntax 3` and `# On-Failure: python3 rollback.py`.
