# e2e scenarios

Whole-workflow tests for swim. A scenario writes real lane scripts into a scratch
git repo and runs them through `swim run`, using the binary built from this
checkout. It then checks the outcome: which lanes passed, failed or were skipped,
and anything the lanes recorded. The Go tests in `internal/e2e` check commands
one at a time. Scenarios check the scheduler, dependencies and lane library together,
at scale.

```sh
make e2e                         # Go e2e suite + all scenarios
make e2e S=dag99                 # just some scenarios
e2e/run.sh -l                    # list
e2e/run.sh -k dag99              # keep the scratch repo to dig into logs
```

`make e2e` / `go test ./internal/e2e` (and so `make check` and CI) runs every
scenario as a subtest, against the binary the Go suite builds; `go test -short` skips them.

Everything runs offline. `lib/fakebin/curl` is first on PATH and serves
`fixtures/pages/`. Each request sleeps `E2E_CURL_DELAY` seconds (default 0.01).
Simulated work in lanes is 0-10ms. To hunt a race that real, slower fetches
would expose, widen the window: `E2E_CURL_DELAY=0.7 e2e/run.sh dag99`.
Set `SWIM_E2E_BIN=/path/to/swim` to test a prebuilt binary.

## Scenarios

| name | what it checks |
|---|---|
| `dag99` | 99 lanes: real-work lanes 1-9, generated 10-98 (fan-out, 15-lane chain, 13-parent fan-in, random layers, 5 independent roots), audit at 99. Every edge finished-before-started, every hash value recomputes from the graph, roots start together. Reports scheduler latency and overhead. |
| `dag99-cascade` | Same graph with lanes 23, 38 and 41 made to fail (`E2E_FAIL`). The skipped set must equal exactly their descendants, and everything else must pass. |
| `dag99-parallel` | Same graph with `--parallel 8`: the audit measures how many lanes ran at once (never over 8), and every dag99 edge and value check still holds. |
| `dag99-locks` | Same graph with `# Locks:` from a pool of five on the generated lanes: the audit checks that no two lanes sharing a lock ever overlapped, and every lane must pass (lock waits never cause skips). |
| `dag99-retry` | Same failures through `swim all`, then a retry with them fixed. `swim plan` must predict the retry (`0 to run, 70 to retry`), `swim all` must rerun exactly the failed and skipped lanes and never one that passed, and a third `swim all` must find nothing to run. |

## Adding a scenario

Create `scenarios/<name>/scenario.sh`. `run.sh` sources it in a subshell whose
working directory is a fresh repo, after `swim init` and `lib/common.sh`:

```bash
DESCRIPTION="one line for -l and the report"
LANES=3                                    # configured with swim config --lanes

scenario_setup() {                         # write lane.N.sh files here
  cp "$SCENARIO_DIR"/lanes/lane.*.sh .
}

scenario_run() {                           # optional; default runs lanes 1..LANES
  SOME_FLAG=1 swim_run 1 2 3               # output in $OUT/run.out, exit in $RUN_EXIT
}

scenario_check() {                         # non-zero fails the scenario
  expect_eq    "exit"   "$RUN_EXIT" 0
  expect_eq    "passed" "$(lanes_with PASS)" "1 2 3"
  expect_match "lane 3 saw its input" "$(step_output 3 'read input')" '^ok'
  info "anything worth printing in the report"
  checks_passed
}
```

Helpers (`lib/common.sh`): `swim_run`, `lane_results`, `lanes_with
PASS|FAIL|SKIP`, `step_output N LABEL`, `expect_eq`, `expect_match`, `info`,
`checks_passed`, `seq_list A B`. `e2etool descendants N...` gives the lanes
downstream of N, read from the lane scripts' `# After:` lines. A scenario can
reuse another's setup by sourcing its `scenario.sh` and overriding functions
(see `dag99-cascade`).

## e2etool

Scenarios need only bash and Go. The work inside lane scripts, and the graph
queries scenario checks make, are done by `e2etool` (`e2e/cmd/e2etool`), which
`run.sh` builds next to the swim binary and puts on `PATH`. `e2etool` with no
arguments lists its commands:

| command | used by |
|---|---|
| `now` | every lane: start and end timestamps |
| `parents-check P...`, `node-value P...` | every generated lane: freshness gate (parents in this run, from `SWIM_RUN`/`SWIM_RUN_LANES`), data-flow value |
| `aggregate`, `markdown`, `stats`, `stats-pages`, `report`, `links`, `wordfreq`, `reconcile`, `domains`, `headers` | dag99 lanes 1-9's real work |
| `audit` | lane 99 |
| `descendants N...`, `edges` | scenario checks |
| `gen DIR` | dag99 setup: writes lanes 10-99 |
| `lock-lanes` | dag99-locks setup: adds `# Locks:` to lanes 10-98 |

Inputs come from the variables lane scripts export (`D`, `N`, `P`, `IN`, `OUT`,
`H`, `NODE_START`, `RUN_ID`, `SWIM_JOB`). To give a new scenario's lanes more
work, add a command in `e2e/cmd/e2etool`, not an inline interpreter.

Lane scripts inside a scenario are source files. A `.gitignore` exception keeps
them tracked, even though swim's own block ignores `lane.N.sh` in working repos.
Keep `run.sh` and `lib/common.sh` compatible with macOS bash 3.2.

## dag99 internals

- `lanes/lane.1.sh`–`lane.9.sh` do real work: fetch, Markdown, stats, links,
  word frequency, a reconcile between branches, headers, a bundle. Their
  `# After:` lines form the first DAG.
- `e2etool gen` writes lanes 10–99 with fresh job ids from `parts/`. The
  graph itself is a fixed table in `e2e/cmd/e2etool/gen.go`, so the expected
  counts (197 edges, 89 values, roots 1 8 20 21 22) never drift:
  - `node.tmpl`: a generated lane
  - `parent_check.sh`: each parent must have finished, for this run, before this lane started
  - `derive.sh`: a lane takes its run id from its parents
  - `audit.sh`: lane 99's checks
- Lanes share `.scenario/dag/` in the scratch repo: `nodes/N.done`
  (run, start, end; the run is swim's `SWIM_RUN`) and `nodes/N.val`
  (sha256 over the parents' values).
- `E2E_FAIL="23 41"` makes those lanes fail on purpose. It is a test switch, not a guard flag.
