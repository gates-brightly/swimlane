# swim observe: what the runs say about the work, codified

Status: in progress. Built: the detectors `timeout`, `retried`, `waited`,
`slow`, `fast`, `failing`, `dry` and `drift` (`internal/observe`, pure,
table-tested), computed at run end into `.swim/observations/<run>.yml`
(a failure is a `warn` in `.swim.log`, never a failed run), `swim
observe [RUN|N|JOB] [--yaml]`, `swim report`'s observations section, and
`swim.yml` `observe:` thresholds. Baselines come from each lane's own log
and archives (by step label), read at run end.

Left: step keys and targets (`--key`, `--target`, `# Targets:`), an index
instead of re-reading logs, matrix cells and jobs, `held-up` and `trend`,
`swim status` and `swim plan` lines, knowledge (accumulate, retire,
accept, reject), and the finally-lane write-up (`SWIM_OBSERVATIONS`,
`swim observe check`).

## Summary

After every run, swim computes **observations** from what it already
records: steps much slower than their usual time, time spent waiting on
timeouts, locks and dependencies, steps that pass only after retries,
targets that keep failing, guards that keep running dry. They are facts with
numbers and citations, computed without an LLM, stored beside the logs, and
shown to the planner in `swim report`.

Observations that keep recurring accumulate into **knowledge** per step and
per target. They retire on their own when behaviour changes. An operator
can promote one into a committed `knowledge/` file the next session reads.

Optionally, a [finally lane](../README.md#dependencies-parallelism-and-locks) hands the run's observations to
Claude to **write them up** (a run report, a runbook note) as one of the
lane's outputs. Claude only writes artifacts. It never decides what a round
does.

```
$ swim observe r-0915
swim observe · run r-0915 · 4 lanes · 3 observations

slow      swim 2  "tf apply orders"           14m02s   usual 3m10s (p90 4m05s, 12 runs)    job=d7af1473
timeout   swim 3  "wait for stack"            timed out at its step limit 5m (exit 124); 2nd of the last 5 runs
flaky     target orders-api                   3 of 4 calls needed retries this run (exit 255, 7 retries);
                                              9 of the last 20 runs retried            swim 1, swim 4
known     target orders-api   flaky since 2026-10-02 (accepted in knowledge/targets.yml by zach)
```

## Motivation

- **The planner relearns the same things every session.** "The orders apply
  is slow on Mondays", "the stack wait needs 10 minutes, not 5", "orders-api
  drops connections under load". Each of these is discovered by an agent
  reading logs, then lost when the session ends. That's the brief's "work
  keeps restarting from zero", applied to experience, not just results.
- **The data is already recorded.** Each step's duration and clock are on
  its result line. Retries are logged as `-- attempt 1/4` / `-- exit 255
  (3.0s); retry in 5s`. Timeouts show as `(timeout: step limit 2m)`.
  `.swim/runs/RUN.yml` records ready, queued, locked, start and end per
  lane, and `swim timeline N|JOB` already builds a lane's history across runs
  (`timeline.HistoryOf`, `internal/timeline/timeline.go:294`, with
  `quantile`). Nothing new needs capturing; it needs computing and keeping.
- **Facts should be computed, not guessed.** "This step took much longer
  than normal" is a comparison against a baseline: exact, reproducible,
  and free to run offline on a headless worker. An LLM reading raw logs to
  find it costs tokens, varies between runs, and can be wrong in ways
  nobody checks.
- **Prose is still worth having.** A run report or runbook note that groups
  the facts and suggests causes is what a person reads. That's the part
  Claude is good at, and it's safest as an ordinary, approved round that
  starts from computed facts.

## Behaviour

### Observations

Computed for every lane in a run once it ends, and for the run once it
ends. Each is a fact with the numbers behind it and where it came from.

| Kind | Fires when | Cites |
|---|---|---|
| `slow` | a step's duration is over `slow.factor` (1.5) × its p90 **and** over its median + `slow.min` (30s), with at least `baseline.min` (5) earlier runs | duration, median, p90, n, job |
| `fast` | under 0.25 × median with the same history (a skipped step often looks like this) | as above |
| `timeout` | a step hit its `--timeout`, or the round hit `Timeout:` | the limit, the step, how often in the last `window` (20) runs |
| `waited` | a lane spent over `wait.min` (2m) waiting, by cause: dependency, slots, lock, queue, approval prompt | the cause, the time, what it waited on (from `RUN.yml`) |
| `retried` | a step needed retries to pass | attempts, exit codes, time lost to backoff |
| `flaky` | a step or target needed retries, or failed then passed on rerun, in at least `flaky.share` (25%) of its last `window` runs | the counts and the runs |
| `failing` | a step or target failed in its last `failing.streak` (3) runs | the runs |
| `dry` | a guard ran as a dry run in its last 3 runs (approved by nobody) | the guard, the runs |
| `drift` | the same `DRIFT` recurred | the line, the runs |
| `held-up` | a lane was on the run's longest chain in its last 3 runs (`swim timeline`'s critical path) | the chain |
| `trend` | a lane's round duration rose more than 50% across its last `window` runs | first and last medians |

- **Rounds that don't repeat get no baseline.** Rounds are disposable, so
  most one-off rounds only get `timeout`, `waited` and `retried`, which need
  no history. Baselines come from work that recurs: Always lanes, group
  files, CI lanes, recurring schedules
  (`swim schedule --daily`), and steps reused across rounds.
- **Thresholds** live in `swim.yml` under `observe:`. They're not policy,
  so they don't enter the manifest, and changing them stales nothing.
- **Never in the lane log.** Observations are derived, so they're written
  next to the logs, not into them. The chain hashes each round's log
  section, and appending to it would break `swim verify`.

### Step keys and targets

A baseline is only as good as its matching across rounds.

- **Step key:** by default, the lane's group or Always identity (or the
  lane number for numbered lanes), plus the label, plus the command
  normalised: argv with UUIDs, hashes, timestamps, `$SWIM_JOB` values and
  temp paths replaced by placeholders. `run --key orders-apply ...` sets it
  explicitly, so a relabelled step keeps its history.
- **Target:** what a step talks to, so flakiness groups by service, not by
  step. Declared with `run --target orders-api ...` (any step kind), or
  `# Targets:` in the header for a lane's default. swim also infers a few
  shapes from argv: a URL's host for `curl` and `wget`, `aws <service>` with
  `--region` or the profile's region, and `terraform -chdir=<dir>`. Inferred
  targets are marked `inferred` and never promoted to knowledge without a
  declared one agreeing.
- **Both are recorded** on the step's block (`key:` / `target:` context
  lines, 8-space indented, so never results), and in `swim log --yaml`.

### Where they're kept

| Path | What |
|---|---|
| `.swim/observations/<run>.yml` | `swim.observations/v1`: every observation of that run, with citations |
| `.swim/observations/index/` | rolling statistics per step key and target (last `window` durations, outcomes, retries), updated at run end, so a run doesn't re-parse old logs. It survives `swim archive`, `swim clean`, and `swim prune` of old run stages (`swim prune --help`). |
| `.swim/knowledge/` | accumulated, unreviewed knowledge (below) |
| `knowledge/` (repo, committed) | knowledge someone accepted |

### Reading them

- **`swim observe [RUN|N|JOB] [--yaml]`**: the observations of the last run,
  or a given run, lane or job.
- **`swim report`** gets an `observations` section
  for runs since the earmark. This is the main way a planner sees them: one
  line each, in the same call that brings the logs.
- **`swim status`** shows a lane's open observations under it
  (`observed: slow "tf apply orders" 14m vs 3m10s`).
- **`swim plan`** shows knowledge that applies to the lanes about to run
  (`known: orders-api flaky (retry 255)`). That's the moment a planner or
  operator can still act on it: add `--retry`, raise a step limit.
- **Finally lanes** get `SWIM_OBSERVATIONS`, the path to the observations
  for every upstream lane that has ended (see [finally lanes](../README.md#dependencies-parallelism-and-locks)).

### Knowledge

Observations are per run. Knowledge is what keeps being true.

- **Accumulated automatically** into `.swim/knowledge/` when an observation
  of the same kind, key or target recurs in at least `knowledge.min` (3) of
  the last `window` runs. An entry holds the claim, its kind, its subject,
  its first and latest evidence (runs and jobs), and its counts.
- **Retired automatically** when the evidence stops: a `flaky` target with
  10 clean runs, a `slow` step back under its p90 for 5 runs. Retired
  entries stay in the file, marked `retired` with the run that retired them.
- **Accepted by a person.** `swim observe accept <id> [--note TEXT]` copies
  an entry into the committed `knowledge/` (`steps.yml`, `targets.yml`)
  with who accepted it and when. That's the knowledge the next session and
  other machines see. `swim observe reject <id>` keeps it from being proposed
  again. Accepted entries still retire on evidence, and `swim doctor` lists
  accepted entries whose evidence retired them, for someone to remove.
- **Read by people and planners, never by swim's decisions.** No knowledge
  entry changes a timeout, a retry, a skip, or an approval. It's shown in
  `swim plan` and `swim report`; acting on it is a script change, made and
  approved like any other.

### The write-up (Claude, optional)

A repo that wants prose adds a finally lane that turns observations into
an artifact, with the built-in `claude` and `report` modules (already
built; see MODULES in `swim --help`):

```bash
# swim: syntax 4
# Round: write up run observations
# Finally: 1 2 3
# Modules: claude report
# Includes: prompts/run-writeup.md
# Outputs: writeup.md
# Guards: OBSERVE_ALLOW_WRITEUP  send this run's observations and masked excerpts to Claude
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 9

stage verify
if guard OBSERVE_ALLOW_WRITEUP "send run $SWIM_RUN's observations and masked excerpts to Claude"; then
  mod claude --prompt-file prompts/run-writeup.md --input "$SWIM_OBSERVATIONS" --out writeup.md -- claude
fi
mod report --optional writeup.md
summary
```

- **An ordinary round, by design.** The write-up is a pinned script in the
  manifest, behind a guard naming the egress, approved up front for queued
  and recurring runs, and logged. Its artifact is a lane `output`, hashed in
  the END block's `outputs:` line and recorded in the chain. This keeps
  the write-up out of hidden hooks on the executor.
- **It starts from facts.** Its input is `swim.observations/v1` plus
  excerpts swim selects for each observation: the cited step's command and
  the last lines of its output, already masked (swim's masker runs before
  anything is written). Not whole logs.
- **The credential can be a guarded secret** (`swim.yml` `secrets:`,
  released only after the lane's guard is approved): the `claude` module
  asks swim's secrets for `ANTHROPIC_API_KEY` when the lane hasn't one,
  else the CLI uses its own login.
- **Write-only to swim.** Its output is an artifact for people and
  planners. swim never reads it back: not into knowledge, not into
  decisions. A write-up may propose knowledge in its text; only `swim
  observe accept` on a computed entry adds any.
- **Every claim cites.** The write-up's prompt asks it to cite observation
  ids for every claim, and to label hypotheses as hypotheses. `swim
  observe check <file>` lists any cited id that doesn't exist in the run's
  observations, so a reviewer can spot an invented citation.
- **No planner, no problem.** In an interactive session the planner
  already reads observations through `swim report`. It can write the same
  artifact itself, during planner-workflow step 6, with no egress from the
  executor at all. The finally lane is for unattended runs: queued,
  scheduled, recurring, headless.
- **It calls Claude through the built-in `claude` module**: the Claude
  Code CLI the call names, with no tools, no MCP, a fixed system prompt
  and the model the call pins, from an empty directory; masked inputs in,
  one Markdown artifact out, never read back. `mod report` prints it into
  the log. What's left for observe is the input: `$SWIM_OBSERVATIONS` and
  the excerpts swim selects, as files the call passes with `--input`.

## Design

| Area | Change |
|---|---|
| `internal/observe` (new) | Detectors over a round (`logparse.Round`), its run record (`runrec.Run`) and the index; `Observation` and `swim.observations/v1`; thresholds from `swim.yml` `observe:`. Pure functions, with no I/O beyond the index. |
| `internal/observe/index` | Rolling per-key and per-target statistics: update at run end, read for baselines, rebuild from logs and run records (`swim observe --rebuild`). |
| `internal/observe/knowledge` | Accumulate, retire, accept, reject; `.swim/knowledge/` and committed `knowledge/` (YAML). |
| `internal/step`, `internal/logparse` | `--key` and `--target` on steps; `# Targets:`; argv normalisation and target inference; `key:` / `target:` block lines parsed into `Result`. |
| `internal/launcher` | After each lane ends, compute its observations; when a finally lane starts, write the partial file and set `SWIM_OBSERVATIONS`; at run end (beside `writeRunRecord`, `launcher.go:964`), the run's observations, the index update and knowledge accumulation. Errors here are logged as WARN in `.swim.log` and never fail a run. |
| `internal/cli/observe.go` (new) | `swim observe [RUN|N|JOB]`, `accept`, `reject`, `check`, `--rebuild`, `--yaml`. |
| `internal/cli/report.go`, `status.go`, plan | The observations section, `observed:` lines, `known:` lines. |
| `internal/doctor` | The index's size; accepted knowledge that retired. |
| `internal/help`, README | An OBSERVATIONS section; planner workflow steps 5–7 point at `swim report`'s observations and at `swim observe accept`; `swim.observations/v1` in the YAML list. |

### Implementation steps

1. **Step keys and targets.** `--key`, `--target`, `# Targets:`, argv
   normalisation and inference, the block lines, and `--yaml`. Unit tests
   for normalisation (UUIDs, hashes, temp paths) and each inferred shape.
2. **The index.** Rolling statistics per key and target, built from
   existing logs and run records (`--rebuild`), then updated incrementally.
   Prove `--rebuild` and incremental give the same index.
3. **Detectors**, one at a time, each as a pure function with a table test:
   `timeout`, `waited` and `retried` first (no history needed), then `slow`,
   `fast`, `flaky`, `failing`, `dry`, `drift`, `held-up`, `trend`.
4. **Run integration.** Compute at lane end and run end; write
   `.swim/observations/<run>.yml`; failures never fail the run.
5. **`swim observe`** and `--yaml`.
6. **Surfaces:** `swim report`, `swim status`, `swim plan`.
7. **Knowledge:** accumulate, retire, accept into committed `knowledge/`,
   reject; `swim plan` shows accepted entries.
8. **Finally integration:** `SWIM_OBSERVATIONS` (a file a finally lane
   reads, and passes to `mod claude --input`), per-observation masked
   excerpts beside it, `swim observe check`. Add an example write-up lane
   under `docs/`.
9. **Docs.**

## Open questions

- **Where does committed knowledge live?** `knowledge/` at the root, or
  under a configurable path? Is it one file per kind, or one per step key and
  target (fewer merge conflicts between operators)?
- **Should accepted knowledge be signed?** Approvals are signed so nobody
  can forge one. Knowledge changes no decision, so plain commits may be
  enough. But if a planner trusts it, a forged "this step is always slow,
  ignore it" could hide a problem.
- **Target inference breadth.** Beyond `curl`/`wget`/`aws`/`terraform`, is
  inference worth it, or should targets be declared only, with `swim lint`
  suggesting `--target` on commands that look networked?
- **Index across machines.** Each worker keeps its own index, so two
  operators see different baselines. Should the hub aggregate indexes per
  repo? It already sees each worker's runs.
- **Observations for direct runs.** A lane run with plain `bash lane.3.sh`
  has no run record, only its log. Should it get step-level observations
  only, or none?

## Testing

- **Unit, detectors:** each kind against fixture rounds and run records,
  including the edges: exactly `baseline.min` runs, a step with no history
  (only history-free kinds fire), a retried step that ultimately failed
  (`retried` and `failing`, not `flaky`).
- **Unit, normalisation:** two rounds whose commands differ only in a UUID,
  a temp path and `$SWIM_JOB` share a key; a relabelled step with
  `--key` keeps its history.
- **Unit, knowledge:** an entry appears after 3 of the last 20 runs, retires
  after its clean streak, and an accepted entry is reported by
  `swim doctor` once its evidence retires it.
- **e2e, run integration:** a lane with a sleep step run 6 times at 1s and
  once at 5s produces one `slow` observation citing the right job; `swim
  report` shows it; `swim verify` still passes (no log was touched).
- **e2e, flaky target:** a step with `--retry 3 --target fake-api` against
  a fixture that fails its first attempt every other run produces `retried`
  each time, `flaky` once over `flaky.share`, and a knowledge entry after
  `knowledge.min`.
- **e2e, never fails a run:** with the index directory unwritable, the run
  passes, and `.swim.log` has a WARN.
- **e2e, finally write-up:** a finally lane gets `SWIM_OBSERVATIONS` with
  its upstream lanes' observations; a stub write-up script's output is
  recorded in the END block; `swim observe check` flags an invented id.
- **Masking:** a secret in a cited step's output never appears in the
  excerpts handed to the finally lane.

## Out of scope

- Claude, or any model, deciding what a round does, or changing timeouts,
  retries, approvals or skips. Knowledge informs people and planners only.
- Built-in Claude calls on the executor outside a lane.
- Anomaly detection beyond the listed kinds, and metrics export
  (Prometheus, Datadog). The index is plain YAML that an exporter could
  read later.
