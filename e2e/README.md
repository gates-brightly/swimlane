# e2e scenarios

Whole-workflow tests for swim, in `internal/e2e/scenarios_test.go`. A
scenario writes real lane scripts into a scratch git repo and runs them
through the swim binary the Go suite builds. It then checks the outcome:
which lanes passed, failed or were skipped, and anything the lanes recorded.
The other Go tests in `internal/e2e` check commands one at a time. Scenarios
check the scheduler, dependencies and lane library together, at scale.

```sh
just e2e                                         # the Go e2e suite, scenarios included
go test ./internal/e2e -run TestScenarioDag99$   # one scenario
```

`go test -short` skips them. This directory holds what the scenarios' lanes
use: their fixed lane scripts (`scenarios/<name>/lanes`), the pages they
fetch (`fixtures/`), the fake curl (`lib/fakebin/curl`, first on `PATH`, so
everything runs offline) and `e2etool`, which does their work. Simulated
work is instant: the scenarios test the scheduler, not the clock, and wait
for events (a file, a process exiting, an event in the run's stream), never
for a time. Scenarios run with `SWIM_CHECK_STATES=1`: an illegal
`status.yml` state transition panics swim ([state machines](../docs/reference/state.md)).

## Scenarios

| test | what it checks |
|---|---|
| `TestScenarioDag99` | 99 lanes: real-work lanes 1-9, generated 10-98 (fan-out, 15-lane chain, 13-parent fan-in, random layers, 5 independent roots), audit at 99. Every edge finished-before-started, every hash value recomputes from the graph, roots start together. Logs scheduler latency and overhead. |
| `TestScenarioDag99Cascade` | Same graph with lanes 23, 38 and 41 made to fail (`E2E_FAIL`). The skipped set must equal exactly their descendants, and everything else must pass. |
| `TestScenarioDag99Parallel` | Same graph with `--parallel 8`: the audit measures how many lanes ran at once (never over 8), and every dag99 edge and value check still holds. |
| `TestScenarioDag99Locks` | Same graph with `# Locks:` from a pool of five on the generated lanes: no two lanes sharing a lock ever overlap, and every lane passes (lock waits never cause skips). |
| `TestScenarioDag99Retry` | Same failures through `swim all`, then a retry with them fixed. `swim all` reruns exactly the failed and skipped lanes and never one that passed, and a third `swim all` finds nothing to run. |
| `TestScenarioDag99Interrupt` | One Ctrl-C while lane 1 holds mid-run, when a random lane starts or ends: every lane ends passed, interrupted at a step boundary, or skipped; no step is cut off mid-command, and no lane starts after the signal. |
| `TestScenarioDag99Force` | Ctrl-C twice with `interrupt_grace` 1s: swim exits, and every lane process is gone, within the grace plus 2s (scaled by `E2E_SLOW`). |
| `TestScenarioCIGitHub` | The failing graph under `swim ci` as GitHub Actions: an `::error` for each failed lane, one notice for all the skipped lanes, failed lanes' sections left open, a job summary row and a JUnit suite for each of the 99 lanes, results tagged with the commit. |
| `TestScenarioCIChanged` | dag99 passes, then a commit touches lanes 30 and 60: `swim ci --changed=<base>` runs exactly those two. |
| `TestScenarioBlocked` | swim blocks only a repo's `blocked_commands` (here `terraform destroy`): a literal match refused before the lane starts, one built at runtime refused at the step. Blocked lanes fail and dependents are skipped; a lane that runs `git commit` passes and its commits land. |
| `TestScenarioFlaky` | Steps that fail their first N calls: `--retry 2` passes on attempt 3/3 when N is 2 and fails at 3/3 when N is 3; every attempt is in the log, and the failed lane's dependent is skipped. |

On macOS, `-Locks`, `-Retry` and `-Parallel` are skipped: they test the
scheduler on 99 lanes, which is the same there, and took 100-140s each on a
macOS runner. `SWIM_E2E_ALL_SCENARIOS=1` runs them anyway.

## dag99

- `scenarios/dag99/lanes/lane.1.sh`–`lane.9.sh` do real work: fetch,
  Markdown, stats, links, word frequency, a reconcile between branches,
  headers, a bundle. Their `# After:` lines form the first DAG.
- `internal/e2e/dag99_test.go` writes lanes 10–99 with fresh job ids. The
  graph is a fixed table there, so the expected counts (197 edges, 89
  values, roots 1 8 20 21 22) never drift.
- Lanes share `.scenario/dag/` in the scratch repo: `nodes/N.done`
  (run, start, end; the run is swim's `SWIM_RUN`) and `nodes/N.val`
  (sha256 over the parents' values).
- `E2E_FAIL="23 41"` makes those lanes fail on purpose. It is a test
  switch, not a guard flag. `E2E_HOLD=<file>` holds lane 1 until the file
  exists, so a test can signal swim while the run is certainly going.

## e2etool

The work inside the lanes is `e2etool`'s (`e2e/cmd/e2etool`), which the
tests build next to the swim binary and put on `PATH`. `e2etool` with no
arguments lists its commands:

| command | used by |
|---|---|
| `now` | every lane: start and end timestamps |
| `parents-check P...`, `node-value P...` | every generated lane: freshness gate (parents in this run, from `SWIM_RUN`/`SWIM_RUN_LANES`), data-flow value |
| `aggregate`, `markdown`, `stats`, `stats-pages`, `report`, `links`, `wordfreq`, `reconcile`, `domains`, `headers` | dag99 lanes 1-9's real work |
| `audit` | lane 99 |
| `await PATH` | lane 1's `E2E_HOLD` step |
| `gotest-report` | this repo's CI lanes: `go test -json` as text and JUnit |

Inputs come from the variables lane scripts export (`D`, `N`, `P`, `IN`, `OUT`,
`H`, `NODE_START`, `RUN_ID`, `SWIM_ROUND_ID`). To give a scenario's lanes more
work, add a command in `e2e/cmd/e2etool`, not an inline interpreter.

Lane scripts here are source files. A `.gitignore` exception keeps them
tracked, even though swim's own block ignores `lane.N.sh` in working repos.
They must work on macOS's bash 3.2.
