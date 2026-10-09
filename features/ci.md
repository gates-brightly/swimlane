# swim ci

Status: shipped (4.20261009)

## Summary

`swim ci` runs rounds inside a CI/CD job (GitHub Actions, GitLab CI, or any
runner that sets `CI=true`). It behaves like `swim all`, but its output is
written for a CI log viewer rather than a terminal:

- verbose: every step's header goes to stdout, not just to the log
- grouped: each lane's log folds into its own section
- annotated: failures and drift show up as annotations
- summarized: a job summary and a JUnit report

It is also built for commits: it can run only the rounds a commit changed, and
it tags every result with the commit SHA.

```sh
swim ci                       # every pending round, CI output
swim ci --changed             # only rounds whose lane scripts changed in this push / MR
swim ci 3 7f2a9c1e            # these lanes / jobs
swim ci --junit swim.xml      # also write a JUnit report
```

## Motivation

Today `swim run` in CI already falls back to plain output (stdout isn't a
terminal), but:

- **Step detail is hidden.** Only step output reaches stdout. The step header
  (the command, cwd, git, env) and the PASS/FAIL lines are in
  `.swim/logs/agentN.log`, which is gone when the runner is torn down unless
  someone uploads it.
- **Parallel lanes interleave.** Their output is mixed line by line, with no
  way to fold one lane away.
- **Nothing reaches the CI UI.** There are no annotations, job summary or test
  report; a failed round shows up only as a red job and an exit code.
- **Nothing ties a run to a commit.** Every push reruns every pending round,
  and nothing links a round's result back to the commit that triggered it.

## Behaviour

### Provider detection

| provider | detected by | grouping | annotations | summary |
|---|---|---|---|---|
| GitHub Actions | `GITHUB_ACTIONS=true` | `::group::` / `::endgroup::` | `::error` / `::warning` / `::notice` | Markdown to `$GITHUB_STEP_SUMMARY` |
| GitLab CI | `GITLAB_CI=true` | `section_start` / `section_end` (collapsed) | none (GitLab has no equivalent; see JUnit) | JUnit via `artifacts:reports:junit` |
| generic | `CI=true`, or `--provider generic` | `==== swim N ... ====` banners | `swim: error: ...` lines | text summary (as `swim run` today) |

`--provider github|gitlab|generic` overrides detection. Running `swim ci` outside
CI works, using generic output, so it can be tried locally.

### Output

1. **Header:** swim version, provider, commit (`<sha>` and branch or MR), the
   lanes selected and why (`--changed: lane.3.sh, lane.7.sh changed since <base>`),
   and the guard flags that are set (names only, never values).
2. **Live stream:** all lanes, interleaved and prefixed `[N]` as in plain mode
   today. Also included:
   - step headers (`[3] === STEP lint`, `[3] $ npm run lint`)
   - result lines (`[3] PASS  lint`)
   - a heartbeat, `[3] still running: <step> (5m02s)`, every 60s for each lane
     with no output, so the job doesn't look hung
3. **Per-lane sections:** after the run, each lane's full round log in lane
   order, one collapsed group per lane. The title reads
   `swim 3 PASS - <round> (job 3f2a9c1e, 0:42)`, and failed lanes are left
   expanded. This gives a clean, unmixed log per lane to complement the live
   stream.
4. **Annotations:**
   - `FAIL <label>` → `error`, titled `swim N: <round>`
   - `DRIFT` → `warning`
   - `SKIP` because of a guard flag (dry run) → `notice`, saying which flag
     would approve it
   - lanes skipped because a dependency failed → one `notice` for the group
5. **Summary:** a table of lane, job, result, pass/fail/skip/drift counts,
   time and failed steps, plus where the logs are. Exit codes:

   | code | meaning |
   |---|---|
   | 0 | every selected lane passed |
   | 1 | a lane failed |
   | 2 | usage, config or an unresolvable `# After:` |

   No pending rounds exits 0 with a notice; `--require-work` makes that exit 1.

### Non-interactive by design

- **No input:** lanes never get stdin, even when only one lane runs. `confirm`
  fails closed, which is already its behaviour without stdin.
- **No prompts:** swim never asks a question, e.g. "Increase lanes to N?". If a
  lane script is outside the configured lanes, `swim ci` fails with the exact
  fix rather than prompting.
- **Guard flags:** these come only from the job's environment or secrets. The
  header lists which are set, so a reviewer can see what a run was allowed to
  do.

### Optimized for commits

- **`--changed[=<base>]`** runs only rounds whose `lane.N.sh` differs between
  `<base>` and `HEAD`, plus any lanes they wait on that haven't passed. The
  default base comes from the provider:

  | provider | event | base |
  |---|---|---|
  | GitHub | push | `github.event.before`, passed as `--changed=$BEFORE` or read from `$GITHUB_EVENT_PATH` |
  | GitHub | pull request | `origin/$GITHUB_BASE_REF` merge base |
  | GitLab | push | `$CI_COMMIT_BEFORE_SHA` |
  | GitLab | merge request | `$CI_MERGE_REQUEST_DIFF_BASE_SHA` |

  An all-zero "before" SHA (a new branch) means every pending round.
- **Commit-tagged results:** the commit SHA is recorded on:
  - the `ROUND START` line, as `commit=<sha>`
  - the `.swim.log` `run` / `pass` / `fail` events
  - `.swim/status.yml` (`commit:`)

  Steps already record `git: <branch>@<sha>` in their headers; this puts it on
  the round itself.
- **Pinned by job:** the selected rounds are resolved to job ids up front and
  run pinned. A lane script rewritten between selection and start is refused,
  the same as `swim run <job>`.
- **Logs as artifacts:** the end of the run prints the artifact paths
  (`.swim/logs/`, `.swim/snapshots/`, the JUnit file). The docs give the
  `upload-artifact` and GitLab `artifacts:` snippets.

## Design

- **Command:** `ci` in `internal/cli/commands.go`. It reuses the `run` path:
  `launcher.Run` with `Plain: true`, `Stdin: nil`, and new `Verbose` and
  `Reporter` options.
- **New package `internal/ci`:**
  - provider detection
  - group, section and annotation formatting
  - finding the `--changed` base
  - the step-summary and JUnit writers

  Each provider is an interface with three methods: `Group(title, collapsed)`,
  `EndGroup()` and `Annotate(level, title, msg)`.
- **Verbose stream:** `internal/step` writes headers and result lines to the log
  only. With `SWIM_VERBOSE=1`, exported by `swim ci` to lanes, it also echoes
  them to stdout. The launcher's `lineWriter` already adds the `[N]` prefix.
- **Sections, annotations, summary, JUnit:** these are built after the run by
  parsing each lane's current round from `.swim/logs/agentN.log` with
  `internal/logparse`. The launcher doesn't need to emit events, and a log
  read back later produces the same report. The JUnit layout is one
  `<testsuite>` per lane and one `<testcase>` per step. A FAIL becomes
  `<failure>` with the step's output tail, and a SKIP becomes `<skipped>`.
- **Heartbeat:** a ticker in the launcher. It uses each lane's last-output time,
  which the display state already tracks.
- **Interruptions:** CI cancels jobs with SIGTERM, then SIGKILL after a grace
  period. The launcher should treat SIGTERM like Ctrl-C: forward it, flush logs,
  record `interrupted`, print the summary. `--timeout <dur>` (per run) stops
  lanes cleanly before the runner's own limit.

## Open questions

1. **Where do CI rounds live?** `swim init` git-ignores `lane.N.sh`, but CI needs
   committed scripts. Options:
   - (a) a tracked lanes directory (`lanes_dir: ci/lanes` in config)
   - (b) `git add -f`
   - (c) a separate file pattern for CI rounds (`ci.lane.N.sh`)

   (a) seems cleanest.
2. **Config on a fresh runner.** `~/.config/swim/config.yml` doesn't exist on
   the runner. Options:
   - work it out: lane count from the highest script number, ordering from
     `# After:` only
   - read a committed `swim.yml`
   - take `--lanes` / `--config`
3. **"Already passed" across runners.** A fresh runner has no
   `.swim/status.yml`, so `swim all` semantics would rerun everything.
   Options:
   - rely on `--changed`
   - restore `.swim/` from the CI cache
   - read the committed `.swim.log` (if a job id has a `pass` event at an
     ancestor commit, skip it)
4. **Results stay on the runner.** `swim ci` never commits or pushes results
   back to the repo; see [blocked-commands.md](blocked-commands.md). The audit
   trail is the uploaded logs and `.swim.log` as an artifact. A workflow that
   wants results committed does that in its own step, outside swim.
5. **Colour.** GitHub Actions renders ANSI colour and GitLab does too. Keep
   `NO_COLOR` respected, but should colour default to on in CI?
6. **Sharding.** Should one large DAG split across several CI jobs
   (`--shard 2/4`, so lanes N where N mod 4 == 1)? That needs cross-job
   dependencies, so probably later.

## Decisions

1. **Where CI rounds live:** the usual `lane.N.sh` at the repo root,
   committed with `git add -f` (or by dropping `lane.[0-9]*.sh` from swim's
   `.gitignore` block). A `lanes_dir` would break `lane_init`, which finds
   the repo root from the script's own directory, and every tool that reads
   `lane.N.sh`. `swim doctor` no longer warns about tracked lane scripts in a
   repo with a committed `swim.yml` (it still warns about tracked `.swim/`
   state).
2. **Config on a fresh runner:** a committed `swim.yml` at the repo root.
   It takes the same keys as a `repos:` entry and applies on top of the
   defaults and under the operator's own repos entry. So operators share it
   too, and unknown keys are an error. If no lanes setting exists anywhere
   (no `swim.yml` lanes and no repos entry), `swim ci` sets the lane count
   from the highest lane script, and passes it to the lanes' own hooks as
   `SWIM_LANES`. Otherwise a script past the configured lanes fails with the
   exact fix, and exit 2.
3. **"Already passed" across runners:** it lives in `.swim/status.yml`. A
   fresh runner reruns every pending round unless `--changed` narrows the
   run, or the job restores `.swim/` from a cache. Reading `.swim.log` for
   passes at ancestor commits isn't built.
4. **Results stay on the runner:** swim never commits or pushes. The docs
   show `upload-artifact` and GitLab `artifacts:` snippets, and `swim ci`
   prints the paths to upload.
5. **Colour:** off. `swim ci` uses plain output, which every CI log viewer
   renders and which keeps the annotations exact.
6. **Sharding:** later.
7. **Verbose stream:** no `SWIM_VERBOSE`. `swim step` already writes the step
   header (`==> label`, `$ command`) and the result line to the terminal, and
   plain mode relays them prefixed `[N]`.
8. **Commit tagging:** `swim ci` exports `SWIM_COMMIT` (HEAD). The commit
   goes in four places, all of them additive, so neither the log grammar nor
   the `.swim.log` field positions change:
   - the round's context line, as `commit: <sha>` (not the `== ROUND` line,
     whose grammar would change)
   - `status.yml`, as `commit:`
   - the end of the details of the `run`, `run-done`, `start` and
     `pass`/`fail`/`interrupted` events in `.swim.log`, as `commit=<sha>`
9. **Pinning:** `swim ci` fixes each selected lane's job id up front, and the
   launcher skips a lane whose script holds a different job when it starts.
   This pin (`launcher.Options.Pins`) now also applies to `swim run <job>`.
10. **`--changed`:** uses `git diff --name-only` (BASE with HEAD, or
    `BASE...HEAD` for a pull request's merge base), in line with "swim only
    reads git". It adds the changed lanes' unpassed dependencies
    transitively. Lanes named by `--changed` run even if their job passed
    before, because the script changed.
11. **Heartbeat:** `--heartbeat D` (default 60s; 0 turns it off). A lane's
    output resets its clock.
12. **Interruptions:** handled by
    [graceful-interrupt.md](graceful-interrupt.md). SIGTERM (a CI cancel)
    stops lanes at step boundaries and forces after `term_grace`. Set
    `term_grace` in `swim.yml` below the runner's own kill timeout. A per-run
    `--timeout` isn't built: rounds have `Timeout:` and steps `--timeout`.
13. **Real CI (dogfooding):** this repo's GitHub Actions workflow is
    `swim ci --junit` over committed rounds at its root: `lane.1.sh` (lint:
    build, gofmt, vet, `swim lint`, shell syntax), then `lane.2.sh` (unit
    tests) and `lane.3.sh` (the Go e2e suite with every scenario) in
    parallel, with settings in `swim.yml`. It runs on Linux and on macOS
    (bash 3.2), and uploads the logs and JUnit report. `make ci` runs the same
    thing locally. The test steps run through `scripts/hermetic`, because
    swim is testing swim from inside a swim lane. The script removes the
    lane's `SWIM_*` variables, its git shim (which refuses the tests'
    `git commit` and would stop the lane) and the CI provider's variables.
    The e2e harness (`TestMain`, `e2e/run.sh`) strips the same variables
    itself.

## Testing

- **Unit:** provider detection from env; golden files for group, annotation
  and section output per provider; JUnit output that parses as XML and matches
  a golden; resolving the `--changed` base for each provider/event env
  combination.
- **Go e2e (`internal/e2e`):** `swim ci` with `GITHUB_ACTIONS=true` and a temp
  `GITHUB_STEP_SUMMARY`; assert groups, annotations, summary contents and exit
  codes. Same with `GITLAB_CI=true` for section markers. Also test `--changed`
  in a scratch repo with two commits that each touch one lane.
- **Scenarios (`e2e/scenarios`):**
  - `ci-github`: dag99-cascade under `swim ci`, checking one `::error` per
    injected failure, a single notice for the skipped group, and a JUnit report
    with 99 suites
  - `ci-changed`: commit a change to two lanes; only those (and their unpassed
    parents) run
- **Real CI:** a workflow in this repo that runs `swim ci` on the e2e fixtures,
  so the GitHub rendering is checked by eye at least once.

## Out of scope

- Running lanes in containers or on other runners.
- A hosted dashboard. The CI provider's UI is the dashboard.
- Changing what `swim run` prints outside CI.
