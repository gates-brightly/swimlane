# Per-step timeouts and retries

Status: shipped (unreleased)

## Summary

`run`, `gate` and `snapshot` take options for a time limit and for retries:

```bash
run  --timeout 2m "tf plan" terraform plan -detailed-exitcode
gate --retry 3 --backoff 5s "stack is ours" bash -c 'aws cloudformation describe-stacks …'
snapshot --timeout 30s "cfn template" aws cloudformation get-template --stack-name orders
```

There's also a round-wide default in the header: `# Step-Timeout: 5m`.

## What exists today

`# Timeout: 30m` limits the whole round:

- `swim step` gets a deadline (`SWIM_DEADLINE`)
- a step still running when it passes is stopped and recorded as
  `FAIL (timeout: Timeout 30m reached)`, with exit 124
- `_swim_after_step` then stops the round

There is no limit on a single step and no retry. Rounds are told to "survive
VPN drops", but each script has to build that itself.

## Motivation

- **A hang wastes the round's time:** one hung step uses up the round's whole
  budget, and anything below it in the DAG waits that long too.
- **Hand-rolled retries are bad for the log:** retrying a flaky AWS call by
  hand (`for i in 1 2 3; do …; done` inside `bash -c`) hides the attempts in
  one step's output and gives one PASS/FAIL for all of them.
- **CI is touchier:** it kills silent or slow jobs, so per-step limits matter
  more there (see [ci.md](ci.md)).

## Behaviour

### Options

Options come before the label:

| option | meaning |
|---|---|
| `--timeout D` | stop the step after D (`30s`, `5m`, `1h30m`). Exit 124, recorded `FAIL <label> (timeout: step limit 5m)` |
| `--retry N` | try up to N more times on a non-zero exit. A step that times out is retried too, unless `--no-retry-timeout` is given |
| `--backoff D` | wait D before the first retry, doubling each time (capped at 5m). Default 2s |
| `--retry-on CODES` | retry only on these exit codes (`--retry-on 1,255`); others fail at once |

### Rules

- **The round's deadline wins:** a step limit never extends past the round's
  `Timeout:`. A retry that couldn't start before the deadline isn't attempted:
  `FAIL (timeout: Timeout 30m reached)`, as today.
- **Default step limit:** `# Step-Timeout: D` in the header applies to every
  step that doesn't set `--timeout`. `--timeout 0` turns it off for one step.
- **`gate` is unchanged:** it stops the round only after the last attempt
  fails.
- **Retries vs safety:** guard flags and confirms aren't retried, and neither
  are BLOCKED steps (see [blocked-commands.md](blocked-commands.md)). The guide
  should say: put `--retry` on read-only and idempotent steps, never on a
  non-idempotent change.

### Log

Each attempt gets indented sub-lines inside the step's block (log syntax 2),
so the log stays complete and parsers ignore them:

```
  PASS  stack is ours (attempt 2/4)               3.8s  15:20:01
        $ bash -c 'aws cloudformation describe-stacks …'
        -- attempt 1/4
        | Could not connect to the endpoint URL
        -- exit 255 (3.0s); retry in 5s
        -- attempt 2/4
        | {"Stacks": [...]}
        -- exit 0 (0.8s)
```

The step's counts in status are unchanged (one PASS or one FAIL).
`--yaml` step events add `attempts: 2`.

## Design

- **Lane library** (`internal/assets/lib.sh`): `run`/`gate`/`snapshot` parse
  leading `--timeout/--retry/--backoff/--retry-on` options and pass them on to
  `swim step` as flags. Keep it bash 3.2 compatible: a `while case` loop over
  `"$1"`, no arrays beyond `"$@"`.
- **`swim step`:**
  - the attempt loop lives in `step.Run`: for each attempt, a context with
    `min(step limit, round deadline)`, kill and exit handling as for the
    current deadline path, then the backoff sleep (interruptible by Ctrl-C)
  - `logparse` learns the `--- attempt` and `--- retry` markers; old logs have
    none, so this is additive
- **Header:** `lane.Info` parses `Step-Timeout:` beside `Timeout:`, and
  `lane_init` exports `SWIM_STEP_TIMEOUT`.

## Decisions

1. **Snapshots keep the last attempt's output**; every attempt is still in the log.
2. **Jitter:** ±20% on every backoff wait.
3. **Retrying a whole lane** is out of scope (`swim all` reruns failed rounds).
4. **Round timeout vs step limit:** both exit 124. When a step ends because of
   the round's `Timeout` (including a retry refused because it couldn't start
   before the deadline), `swim step` leaves `<log>.round-timeout`; the lane
   library sees it and stops the round. A step limit alone doesn't stop the round.
5. `Step-Timeout:` is passed from `_start` to `lane_init` and exported as
   `SWIM_STEP_TIMEOUT`; `swim step --timeout 0` turns it off for one step.
6. Attempt sub-lines are `        -- …` lines in the step block (8-space indent),
   so syntax-2 parsers already ignore them: no syntax bump.

## Testing

- **Unit:** option parsing in lib.sh, run in bash 3.2 if available (CI on
  macOS); the backoff schedule; deadline arithmetic (step limit vs round
  deadline).
- **Go e2e:**
  - `run --timeout 1s "x" sleep 5` → FAIL, exit 124, about 1s
  - `run --retry 2 "x" bash -c 'test -f ok || { touch ok; exit 1; }'` → PASS
    on attempt 2, with both attempts in the log
  - `--retry-on 7` doesn't retry exit 1
  - a retry never starts after the round deadline
  - Ctrl-C during backoff interrupts cleanly
- **Scenario:** `flaky` (shipped): lanes whose steps fail the first N
  times, using a counter file, so that a correct retry config passes and too
  few retries fails with the right attempt counts.
