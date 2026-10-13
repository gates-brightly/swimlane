# Outcome handlers and hard failure

Status: shipped (unreleased)

## Summary

A round can define three handler functions, which swim calls when the round
ends, depending on its outcome:

```bash
on_success() {
  run "tag stack migrated" aws cloudformation update-stack ... --tags Key=migrated,Value=1
}
on_failure() {
  reached change || return 0          # nothing changed, nothing to undo
  if guard ORDERS_ALLOW_ROLLBACK "redeploy SST stack orders-prod"; then
    run "sst deploy" npx sst deploy --stage prod
  fi
}
on_exit() {
  run "remove temp bucket" aws s3 rb s3://orders-migrate-tmp --force
}
```

A round can also choose how a failed step behaves:

```bash
# Fail: hard          # default: soft
```

- **`soft`** (today's behaviour): a failed `run` is logged and the round
  keeps going. It ends FAIL, then `on_failure` runs.
- **`hard`**: the first failed step stops the round and goes straight to
  `on_failure`, like `set -e` but logged, with handlers and a summary.

## Motivation

- **No cleanup hook:** temporary resources (buckets, port-forwards, scale-ups)
  leak whenever a round stops early. Today the only place to clean up is the
  end of the script, which a gate, `stop`, Ctrl-C or timeout skips.
- **No rollback path:** a failed change leaves the operator to work out by
  hand what to undo. A rollback written next to the change, reviewed and
  approved with it, is safer than one improvised during an incident.
- **No `set -e` alternative:** lint forbids `set -e`
  (`internal/lint/checks.go:110`) because it skips logging and the summary.
  Rounds where every step must pass have to write `gate` for every step.

## Behaviour

### Handlers

- **Defining them:** a round defines handlers as bash functions:
  `on_success`, `on_failure` and `on_exit`. All three are optional.
- **When they run:** the library's exit trap (`_swim_on_exit`,
  `internal/assets/lib.sh:63`) calls them on every exit path: the end of the
  script, `summary`, a gate, `stop`, a hard failure, an interrupt, a timeout,
  or a bash error.
- **Order:** `on_success` or `on_failure` runs first, then `on_exit`, like
  try / catch / finally.
- **Which one runs:** `on_success` runs when the round passed (no failed
  steps). Otherwise `on_failure` runs.
- **In the log:** each handler gets its own section (`-- on_failure
  12:04:10`), and steps inside it log as usual. A handler outside the four
  stages is not a stage-order `WARN`.
- **Context for handlers:**

  | Variable | Value |
  |---|---|
  | `SWIM_OUTCOME` | `pass`, `fail` (a soft round finished with failures), `stopped` (gate, `stop`, hard failure, blocked command), `interrupted`, `timeout` |
  | `SWIM_STOP_REASON` | the `STOP` text, if any |
  | `SWIM_FAILED_STEP`, `SWIM_FAILED_STAGE` | the first failed step's label and stage |

- **`reached STAGE`:** a new library function. It's true if the round
  started that stage. A failure during `snapshot` or `check` means nothing
  changed, so an `on_failure` usually starts with `reached change || return
  0`.

### Handlers always run soft

- **Failures don't stop a handler:** a failed step inside a handler never
  stops it, even in a hard round and even if it's a `gate`. Every rollback
  step gets its attempt, because stopping halfway through a rollback is the
  worst place to stop.
- **Failures are loud:** a failing handler is recorded on the `END` line
  (`END FAIL ... on_failure: FAIL`), in status, in `--yaml` and in the chain
  entry.
- **Exit code:** a round whose `on_failure` failed exits `89`, so CI and
  `swim wait` can tell "failed, rolled back" from "failed, rollback failed".
- **Handlers don't change the result:** a round that failed stays FAIL,
  however its handlers did.
- **No recursion:** a handler's own failure doesn't call `on_failure`
  again.

### Guards in handlers

- **Same rules as anywhere:** guards in handlers approve exactly like guards
  elsewhere. An unapproved rollback is a dry run.
- **Approve them up front:** `swim queue --approve ORDERS_ALLOW_ROLLBACK`,
  or a signed approval. Signed approvals cover the script's hash, so they
  already cover its handlers.
- **Shown by plan:** `swim plan` lists each handler's guards, so an
  unattended run with an unapproved rollback isn't a surprise.

### Hard failure

| | `# Fail: soft` (default) | `# Fail: hard` |
|---|---|---|
| A failed `run` or `snapshot` | logged; the round keeps going and ends FAIL | `STOP hard failure: <label>`, then `on_failure` |
| A failed `gate` | stops the round | stops the round |
| A declined guard | dry run (`SKIP`), not a failure | the same |
| `confirm` answered no | stops the round | stops the round |
| One step opting out | — | `run --soft "probe" ...` |

- **No `run --hard`:** `gate` already is a step that must pass. In a soft
  round, use `gate`.
- **Lint's `set -e` error** gets a new fix: "use `# Fail: hard`".
- **Retries apply first:** `--retry` runs every attempt before the step
  counts as failed.

### Interrupts and timeouts

- **First Ctrl-C** (or `swim interrupt N`): the round stops at the step
  boundary and its handlers run.
- **Second Ctrl-C:** interrupts the running handler step too. The remaining
  handler steps are skipped and logged as such.
- **Third Ctrl-C:** kills everything at once. No handlers run.
- **`# Handler-Timeout: 5m`** (default `5m`): the time budget for all
  handlers together. It starts when the round ends, so a round that ran out
  its own `Timeout` still gets time to roll back. When the budget runs out,
  the running handler step is stopped and the rest are skipped.

### Syntax version

- **Bump to `# swim: syntax 3`:** a script that defines a handler or sets
  `Fail:` must declare it. An older swim's library would never call
  `on_failure`, so the round would silently run without its rollback.
  `syntax.Check` already refuses scripts newer than the binary.
- **No migration needed:** syntax 2 scripts migrate to 3 unchanged.
- **Lint:**
  - a script using handlers, or `Fail:`, without syntax 3 is an error;
  - a near-miss name (`on_fail`, `onfailure`, `always`, `cleanup`, `rollback`)
    is an error, because a typo means the handler silently never runs;
  - an `on_failure` that uses a guard but never calls `reached` is a warning.

## Design

- **Library (`internal/assets/lib.sh`):**
  - `_swim_on_exit` computes the outcome, exports the context variables,
    marks a handler section, and calls each handler that `declare -F` finds
    (bash 3.2 safe).
  - A `_swim_in_handler` flag makes `stop`, `gate` and a hard failure
    record a failure and return, instead of exiting.
  - `_swim_after_step` calls `stop "hard failure: $_swim_label"` when the
    mode is hard and the step wasn't `--soft`.
  - `_swim_stage` tracks the furthest stage, for `reached`.
- **Header (`internal/lane/script.go`):** `Fail` (`soft` or `hard`, with
  anything else a problem) and `HandlerTimeout`, beside `Timeout`. Both are
  exported to the lane as `SWIM_FAIL_MODE` and `SWIM_HANDLER_TIMEOUT`.
- **Timeouts:** `swim step` takes the handler deadline in place of the round
  deadline while `_swim_in_handler` is set.
- **Logs and parsing (`internal/logparse`):** handler sections, and
  `on_success:`, `on_failure:` and `on_exit:` results on the `END` line. The
  `swim.log/v1` and `swim.run/v1` schemas gain `handlers` fields (additive).
- **Chain (`internal/chain/chain.go:45`):** an optional `handlers` map
  (name → result). Older entries still verify.
- **Lint (`internal/lint/checks.go`):** the checks above, plus the new
  `set-e` fix text.
- **Syntax (`internal/syntax/syntax.go:30`):** `Current = 3`.

## Open questions (answered)

1. **Outcome naming:** `stopped`, with `SWIM_STOP_REASON` starting
   `hard failure:`. One outcome for every deliberate stop keeps handlers
   simple, and the reason tells them apart.
2. **`on_exit` or `always`:** `on_exit`. `always` is a generic bash name,
   and lint treats `always`, `cleanup` and `rollback` as near misses only
   when a round defines no handler (otherwise they're helpers).
3. **`on_failure` after interrupt:** handlers run on the first Ctrl-C, with
   `SWIM_OUTCOME=interrupted` so a handler can return early. There's no
   extra question: guards in handlers aren't asked once the run is
   stopping (the approval broker declines), so a destructive rollback after
   an interrupt runs only if it was approved up front.
4. **Exit code 89:** kept, for failed rounds only. An interrupted round
   keeps 130/143, and its handler outcome is on the END line.
5. **Breaking version:** no bump. Script syntax 3 (a new `syntax.Script`,
   separate from the log syntax) makes an older swim refuse such scripts,
   and nothing is migrated. A sidecar with handler keys implies syntax 3,
   since an older swim refuses its unknown keys.

Decided while building:

- **`reached STAGE`** is true for that stage or a later one, so a round
  that got to verify also "reached change".
- **Lanes in other languages:** `On-Success:`, `On-Failure:` and `On-Exit:`
  commands (or sidecar keys), run with `sh -c` by the wrapper. A command
  that never calls `swim run` is recorded as one step.
- **A handler that calls `exit`** records STOP and skips the remaining
  handlers. The round still ends normally.
- **The log section line** is `-- handler NAME  hh:mm:ss`, and the END
  block adds `handlers: on_failure PASS | on_exit PASS`.

## Testing

- **Each exit path** (end of script, `summary`, gate, `stop`, hard failure,
  blocked command, first and second Ctrl-C, round timeout, a bash error) calls
  the right handlers in the right order, with the right `SWIM_OUTCOME`.
- **Soft vs hard:** the same failing step continues in a soft round and
  jumps to `on_failure` in a hard one. `run --soft` doesn't stop a hard
  round. A declined guard never counts as a failure.
- **Handlers run soft:** a failing first rollback step doesn't stop the
  second. A gate inside a handler doesn't exit. The round result is
  unchanged, and the exit code is 89 when `on_failure` failed.
- **`reached`:** false before a stage starts, true after.
- **Guards in handlers:** dry run without approval; run with `--approve` in a
  queued batch and with a signed approval; listed by `swim plan`.
- **Handler-Timeout:** a round that exhausted its `Timeout` still runs its
  handlers within the handler budget.
- **Syntax and lint:** an older binary refuses a syntax 3 script; lint
  catches near-miss names, a missing syntax line and `on_failure` without
  `reached`.
- **bash 3.2:** the library changes run under macOS's bash.
- **e2e:** a scenario with hard and soft lanes, injected failures, and
  rollbacks that pass and fail.

## Out of scope

- **Per-step undo** (`run ... --undo "cmd"`, run in reverse order on
  failure). It's worth considering later, but handlers come first.
- **Handlers across lanes:** a downstream lane's failure doesn't trigger an
  upstream lane's rollback.
- **Retrying the round** from a handler.
