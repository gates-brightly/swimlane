# Lanes

Writing a lane script. Start from `swim new N "<goal>"`.

## Forms

- **Short** (what `swim new` writes): first line `#!/usr/bin/env swim`,
  then the header and steps. `swim run` loads the library, calls
  `lane_init N` and ends the round when the script ends. `./lane.N.sh`
  runs it as `swim run N` does; `bash lane.N.sh` can't.
- **Explicit:** loads the library itself and calls `lane_init N` first and
  `summary` last, so it also runs with plain bash:
  `_swim_lib=$("${SWIM_BIN:-swim}" _lib) || exit 1; eval "$_swim_lib"`.

A lane's manifest pins which form it uses (its runner).

## Example

```bash
#!/usr/bin/env swim
# swim: syntax 4
# Round:   Cut over orders-api from SST to Terraform
# After:   1
# Guards:  ORDERS_ALLOW_SST_REMOVE  remove SST stack orders-prod (2026-10-09: tf owns it)
# Locks:   tf/orders
# Timeout: 30m

stage snapshot
snapshot "cfn template" aws cloudformation get-template --stack-name orders-prod
stage check
step  "tf plan" terraform -chdir=infra/orders plan -detailed-exitcode
gate "DeletionPolicy is Retain (deployed)" bash -c 'aws cloudformation get-template --stack-name orders-prod | grep -q Retain'
stage change
if guard ORDERS_ALLOW_SST_REMOVE "remove SST stack orders-prod (2026-10-09: tf owns it)"; then
  step "sst remove" npx sst remove --stage prod
fi
stage verify
step "re-plan is a no-op" terraform -chdir=infra/orders plan -detailed-exitcode
```

## Functions

| | |
|---|---|
| `lane_init N` | Explicit form only, first: cd to the lane's workspace (its pinned copy under `swim run`), open the round, export `SWIM_ROUND_ID`, `SWIM_DEADLINE`, `SWIM_TIMEOUT`. Refuses a lane already running. |
| `stage NAME` | Start a [stage](#stages). Steps before the first are setup. |
| `step [opts] "<label>" cmd [args]` | A step: PASS/FAIL. A failure doesn't stop the round. (`run`, its old name, works for one more release; `swim lint --fix` rewrites it.) |
| `gate [opts] "<label>" cmd [args]` | A step whose failure (after the last attempt) stops the round. |
| `snapshot [opts] "<label>" cmd [args]` | A read-only step whose output is also saved under `.swim/snapshots/`. |
| `guard FLAG "<action - date, reason>"` | True if FLAG is approved for this run, else logs SKIP (a dry run). See [guards](guards.md). |
| `confirm "<question>" [answer]` | The operator must type the answer (default `yes`); anything else, or no stdin, stops the round. |
| `last_failed` / `any_failed` | True if the previous step / any step this round failed. |
| `reached STAGE` | True if the round started STAGE or a later one. |
| `source FILE` | Under `swim run`, a repo file is read from the stage, and only if `# Includes:` lists it. |
| `summary` | Explicit form only, last: log the summary, record the final state, exit non-zero if a step failed. |

**Step options**, before the label:

| | |
|---|---|
| `--timeout D` | Stop the step after D (`30s`, `5m`): FAIL, exit 124. `0` turns off `Step-Timeout:` for this step. |
| `--retry N` | Up to N more tries on a non-zero exit. Only on read-only or idempotent steps. |
| `--backoff D` | Wait before the first retry (default 2s), doubling to 5m, ±20% jitter. |
| `--retry-on CODES` | Retry only on these exit codes (`1,255`). |
| `--no-retry-timeout` | Don't retry an attempt the limit stopped. |
| `--soft` | In a `Fail: hard` round, this step's failure doesn't end it. |
| `--key K` / `--target T` | The step's name across rounds / what it talks to (default: `# Targets:`), both logged. |
| `--expect-fail[=CODES]` | A negative test: PASS on a non-zero exit (one of CODES), FAIL on 0. Not with `--retry`. |

The round's `Timeout:` always wins. A step runs in its own process group,
and a limit stops all of it (SIGTERM, SIGKILL after 5s), except a step
reading the terminal, which stays in the lane's group and is stopped alone.

## Header

`# Key: value` lines at the top (at most one space after `#`). Only
`Round` is required; empty means the default. Other keys (`# Ticket:
OPS-42`) are kept and shown in the log; a value swim can't use is a WARN.

| | |
|---|---|
| `# swim: syntax 4` | The [syntax](compatibility.md#the-syntax-version). |
| `# Round:` | The goal. A script without one isn't run. |
| `# Round-ID:` | The round id, written by `swim new`; keep it. (An older script's `# Job:` reads the same; `swim lint --fix` rewrites it.) |
| `# Task:` | A name for the lane (`swim run checks`). |
| `# After:` | Lanes, tasks or round ids that must pass first. |
| `# Owner:` / `# Created:` | Who wrote it / when. |
| `# Guards: FLAG  <what it approves>` | One per flag: the only guards it can have approved. `# Guards: * <why>` lets it name flags at run time. `swim lint --fix` adds them. |
| `# Locks:` | Resources it needs to itself (`orders-table, tf/orders`). |
| `# Step-Timeout:` / `# Timeout:` | Default limit per step / limit for the round. |
| `# Targets:` | What its steps talk to by default. |
| `# Fail: soft\|hard` | `hard`: the first failed step ends the round. |
| `# Includes:` | Repo files that are part of the round (sourced files, local modules): pinned and staged; the only repo files it may read as code. |
| `# Signoff:` | See [CI](ci.md#local-ci). |
| `# Workspace: live` | Run in the checkout itself, not a pinned copy (for tools that read the whole tree, like `go test`); lint warns (`workspace-live`). |

## Stages

| | |
|---|---|
| `snapshot` | Read-only capture of current state. |
| `check` | Checks and gates; nothing changes yet. |
| `change` | The change, destructive steps behind guards. |
| `verify` | Prove it worked (a re-plan is a no-op). |

In that order; leave out what you don't need. Out of order, twice, or
`change` without snapshot and check before it: a WARN. An unknown name
stops the round. Each stage's result (PASS, FAIL, SKIP, none) is in the END
block and `status.yml`.

## Rules

- No `set -e`: failed checks keep going; only gates stop the round. A
  command outside a step that fails is logged `WARN outside a step` and
  changes nothing: put what matters in a step.
- Steps take argv, not a shell string: `step "count" bash -c 'scan | jq .Count'`.
- Load your toolchain (nvm, asdf, pyenv) yourself: non-interactive shells
  don't.
- Snapshot first. Every destructive step behind its own guard, after a gate
  that tells "ours" (tagged) from "theirs".
- Rerunnable: steps idempotent or detecting their own prior success.
- End with a verification step that prints the evidence (counts, ids, "No
  changes"): it's the part of a passing round anyone reads.
- Check the target first: a gate that stops on the wrong account. For
  Terraform, `snapshot "plan" terraform plan -out tfplan` in check, `terraform
  apply tfplan` inside the guard, so what's applied is what was seen.
- Many similar targets: one step each, fed without a pipe (`while read t; do
  step "drain $t" ...; done < <(list-targets)`); a piped loop runs in a
  subshell and loses its failures (lint: `step-subshell`).
- Keep output streaming: not `out=$(cmd); echo "$out"`, but `bash -o
  pipefail -c 'cmd | awk "{print} /bad/{b=1} END{exit b}"'`. Python CLIs
  buffer when piped: `PYTHONUNBUFFERED=1`.
- A lane runs in a pinned copy holding the script and what `# Includes:`
  pins; `$SWIM_ROOT` is the repo, for what the round acts on.
- Header values take no trailing `# comment`; explain on lines of their own.
- Write for the operator's machine, not yours: often macOS, so bash 3.2 (no
  associative arrays, `mapfile`, `${x,,}`) and BSD tools (`sed -i.bak`; no
  `date -d`, `grep -P`, `stat -c`, `find -printf` or `timeout`: use
  `--timeout`). `swim lint` warns about both (`bash4`, `gnu-only`), and
  `swim doctor` on the operator's machine says which of them it can't run.
- No LLM calls that decide what the round does (branching, gating,
  retries): the script the operator approved decides. One may produce an
  artifact nothing reads back.

## Lint

`swim lint` reports errors, warnings and info with `file:line`, a fix and a
`[code]`; exit 1 on an error (`--strict`: on a warning). `swim run` refuses
`set -e` and a missing or wrong `lane_init`. Accept a finding with a
reason:

```bash
rm -rf "$TMP"   # swim:lint-ignore destructive scratch dir this round made
# swim:lint-ignore bash4 runs only on the Linux CI box          (the next line)
# swim:lint-ignore-file cross-lane reads lane 1's output        (the file)
```
