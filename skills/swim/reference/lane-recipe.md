# A lane script, annotated

Start from `swim new N "<goal>"`, which writes the header, a fresh `# Job:`
id and the library line. Keep the `# Job:` line, and never copy a job id
into another script. Then shape the body like this. Check the details
against `swim guide` (`WRITING A LANE SCRIPT`).

```bash
#!/usr/bin/env bash
# swim: syntax 2
# Round:   Cut over orders-api from SST to Terraform
# Job:     3f2a9c1e-7b4d-4e2a-9c1e-7b4d4e2a9c1e
# After:   1
# Locks:   tf/orders
# Includes: lib/orders.sh
# Guards:  ORDERS_ALLOW_SST_REMOVE  remove SST stack orders-prod (tf owns it) 2026-10-15
# Observe: aws_account = aws sts get-caller-identity --query Account --output text
# Timeout: 30m
#
# Goal: what this round does and why, for an operator reading it without you.
# Steps: snapshot -> checks/gates -> one guarded change -> verify.
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2
. lib/orders.sh

stage snapshot                     # read-only; output saved under .swim/snapshots/
snapshot "cfn template" aws cloudformation get-template --stack-name orders-prod

stage check                        # facts and gates; nothing changes here
gate "account is orders-prod" bash -c '[ "$(aws sts get-caller-identity --query Account --output text)" = 123456789012 ]'
evidence "tf plan" terraform -chdir=infra/orders plan -no-color   # an approval can cover this output
gate "DeletionPolicy is Retain" bash -c 'aws cloudformation get-template --stack-name orders-prod | grep -q Retain'

stage change                       # only guarded steps
if guard ORDERS_ALLOW_SST_REMOVE "remove SST stack orders-prod (tf owns it) 2026-10-15"; then
  run --timeout 20m "sst remove" npx sst remove --stage prod
fi

stage verify                       # prove the outcome, not just exit 0
run "re-plan is a no-op" terraform -chdir=infra/orders plan -detailed-exitcode
summary
```

Header lines take a value and nothing else. A `#` comment after the value
is read as part of it, so explain things in the `# Goal:` block instead.

| Header | Meaning |
|---|---|
| `Job:` | Written by `swim new`. Don't edit it. |
| `After: 1` | Start once swim 1 passes. Skipped if it fails. |
| `Locks: tf/orders` | Never overlap another lane that holds this lock. |
| `Includes: lib/orders.sh` | Every local file the lane sources, so the approval pins it. |
| `Guards: FLAG  <action>` | Each guard flag, with what it approves, the date and the reason. |
| `Observe: name = cmd` | A fact swim can't pin (the account, a binary on PATH). It's read at approval and again before the guarded step, and a difference ends the round as drift. |
| `Timeout: 30m` | The whole round's limit. |

## Step functions

| Call | Use it for |
|---|---|
| `snapshot "<label>" cmd...` | Read-only capture before a change. Its output is saved. |
| `run "<label>" cmd...` | A step. A failure is recorded and the round continues. |
| `gate "<label>" cmd...` | A step whose failure ends the round. Put one before any change it protects. |
| `evidence "<label>" cmd...` | A check whose output (a plan) an approval covers. If the output changes, it asks again. |
| `guard FLAG "<action>"` | Returns 0 only if a person approved this action in this run. |
| `stop "<reason>"` / `drift "<what>"` | End the round, or record a difference from what was expected. |
| `msg --progress 3/25 "<text>"` | A status line in the live view, `swim status` and the hub. |
| `output KEY VALUE` / `input N KEY` | Pass small values (or `# Outputs:` files) to dependent lanes. |

`run`, `gate` and `snapshot` take `--timeout 2m`, `--retry 3`, `--backoff 5s`
and `--retry-on 1,255` before the label. Retry only idempotent steps.

## Patterns

- **Read before you write:** a gate that compares deployed state with what
  you expect stops a round written against stale assumptions.
- **Tell ours from theirs:** tag what you create with `$SWIM_JOB`, and gate
  on the tag, so a rerun doesn't trip over its own resources.
- **Back up before a delete:** snapshot the items or rows, and know the
  restore command. Put that command in the round's comments.
- **Prefer reversible changes:** disable, scale to zero or detach before you
  delete, when ownership is unclear.
- **Probes have no side effects:** no messages, emails, payments or
  mutations. Use health routes, dry runs, or inputs that validation rejects.
- **Stream output:**
  - Avoid `out=$(slow_cmd); echo "$out"`, which shows nothing until the
    end.
  - Check while it streams instead:
    `bash -o pipefail -c 'slow_cmd | awk "{print} / both\$/{bad=1} END{exit bad}"'`.
  - Python-based CLIs (aws, gcloud) buffer when piped: run them with
    `PYTHONUNBUFFERED=1`.
- **Many similar targets:** use one matrix round
  (`# Matrix: SVC=orders,billing,users`), not copy-pasted lanes. Retry only
  the failed cells with `swim run N --failed`.
- **Standing setup** (lint, init): an `# Always: yes` lane. Other lanes name
  it in `After:` only if they need it to pass.

## The same shape in Python

Prefer bash. Reach for another language when the user asks, or when the
work is much easier with a native library (boto3, a database driver,
parsing JSON). `swim new N "<goal>" --lang py` (or `ts`) writes the
template. The SDK gives you the same stages, steps, guards and log. Its
setup and the other languages are in `swim guide` (`OTHER LANGUAGES`)
and in `docs/examples/` in the swimlane repo.

```python
#!/usr/bin/env python3
# swim: syntax 3
# Round:  Archive sessions older than 30 days
# Guards: SESSIONS_ALLOW_ARCHIVE  move sessions older than 30 days to sessions_archive
import sqlite3
import swim

DB = "data/sessions.db"
OLD = "created < datetime('now', '-30 days')"

def count_old():
    return sqlite3.connect(DB).execute(f"select count(*) from sessions where {OLD}").fetchone()[0]

with swim.round() as r:
    r.stage("snapshot")
    r.snapshot("sessions before", ["python3", "-c", f"import sqlite3; print(sqlite3.connect('{DB}').execute('select * from sessions').fetchall())"])

    r.stage("check")
    with r.step("count old sessions", gate=True) as s:   # in-process, logged as a step; gate: failing ends the round
        n = count_old()
        s.log(f"{n} sessions older than 30 days")
        s.require(n < 10000, "suspiciously many: check the cutoff")

    r.stage("change")
    if r.guard("SESSIONS_ALLOW_ARCHIVE", "move sessions older than 30 days to sessions_archive"):
        with r.step("archive old sessions") as s:
            db = sqlite3.connect(DB)
            db.execute(f"insert into sessions_archive select * from sessions where {OLD}")
            s.log(f"deleted {db.execute(f'delete from sessions where {OLD}').rowcount} rows")
            db.commit()

    r.stage("verify")
    with r.step("none left") as s:
        n = count_old()
        s.require(n == 0, f"{n} old sessions remain")
```

- **`r.run(...)` for commands, `with r.step(...)` for in-process work.**
  Both are logged as steps. A bare `subprocess.run` is invisible to swim:
  no log block, no timeout, no blocked-command check, and `swim lint` flags
  it.
- **`gate=True`** makes an in-process step a gate. `s.require(cond, why)`
  fails the step with that reason.
- **The guard works exactly as in bash:** a dry run until a person
  approves.

## Before handing it over

```sh
swim lint N          # fix errors; read the warnings
swim plan            # the tree, the predicted skips, each guard flag
```
