# YAML output for planners and scripts

Status: proposed

## Summary

The commands whose output gets read by planners, workers, CI and the e2e suite
gain a `--yaml` form with a documented, versioned schema:

- **`swim status --yaml`:** exists today; it prints `.swim/status.yml`. It gets
  a `schema:` field.
- **`swim run --yaml` / `swim all --yaml`:** a stream of events, one YAML
  document per event, separated by `---`, then a final `summary` document.
- **Also:** `swim plan --yaml`, `swim log N --yaml` (the parsed round, not the
  raw text) and `swim timeline --yaml` (see [timeline.md](timeline.md)).

YAML matches what swim already writes (`status.yml`, config), and its parsers
are everywhere. Every agent that reads swim results can use one library.

## Motivation

Today, reading results means scraping text written for people:

- **The e2e suite scrapes:** `lane_results` in `e2e/lib/common.sh` parses the
  run summary table with awk, and `step_output` slices log text between
  `--- output` markers.
- **Planners scrape:** the guide tells them to read `agentN.log` and
  `status.yml`; the log grammar is regular, but it's free text.
- **Layout changes break readers:** any change to the table or the log grammar
  quietly breaks every script that reads it.

Swim's main readers are AI sessions, so a stable structured form pays for
itself.

## Behaviour

### `swim run --yaml`

Stdout carries only YAML documents, with no panel and no colour. Lane output
lines become `output` events. That only happens with `--yaml-output`, because
they're noisy; otherwise lane output goes to the logs only.

```yaml
schema: swim.run/v1
event: run
run: r-20261009T141118Z-7c2e        # see run-id.md
lanes: [1, 2, 3]
at: 2026-10-09T14:11:18Z
---
event: start
lane: 1
job: 6885ada8-4cda-4947-98d6-17f37df32cea
round: "DAG root: fetch pages"
at: 2026-10-09T14:11:18Z
---
event: step
lane: 1
label: fetch example
result: PASS            # PASS | FAIL | SKIP | APPROVED | BLOCKED
exit: 0
duration_s: 0.21
---
event: waiting
lane: 3
on: [1]
---
event: finish
lane: 1
result: passed          # passed | failed | skipped | interrupted
exit: 0
counts: {pass: 13, fail: 0, skip: 0, drift: 0}
failed_steps: []
duration_s: 1.4
---
event: summary
run: r-20261009T141118Z-7c2e
result: failed
lanes:
  - {lane: 1, job: 6885ada8-…, result: passed, exit: 0, duration_s: 1.4}
  - {lane: 2, job: 7353ee29-…, result: failed, exit: 1, failed_steps: ["FAIL  convert (exit 1)"]}
  - {lane: 3, job: 127c5d37-…, result: skipped, reason: "swim 2 failed"}
logs: {1: .swim/logs/agent1.log, 2: .swim/logs/agent2.log, 3: .swim/logs/agent3.log}
```

The exit code is the same as without `--yaml`.

### Other commands

- **`swim log N --yaml`:** the latest round, parsed, using the same model as
  `internal/logparse`. It has the header context, then each step with its
  command, cwd, env, output, exit, duration and result, then stages and the
  summary. `--all` gives every round.
- **`swim plan --yaml`:** the tree as data. For each lane: its state, what it
  waits on, and whether it would run, retry or be skipped, with the reason.

### Stability

- **Versioned:** each document carries a `schema:` (`swim.run/v1`,
  `swim.status/v1`, …).
- **Additive changes are allowed:** new fields within a version. Removing or
  renaming a field bumps the version.
- **Documented:** schemas go in `swim --help` under READING RESULTS, next to
  the log grammar.

## Design

- **Event stream:** the launcher already funnels state changes through
  `display.Set` and lines through `display.Line`. A `yamlDisplay` takes the
  place of the live and plain displays and encodes events with `yaml.v3`,
  which is already a dependency. One document per event, flushed immediately,
  so a reader can stream.
- **Step events:** these come from the step result lines that `swim step`
  records in status (the `_step` hook path), not from re-reading logs.
- **`log --yaml`:** marshal `logparse.Round` with yaml tags added; no new
  parser needed.

## Open questions

1. **Should `output` events be included by default?** Recommend off. Output is
   in the logs, and `swim log N --yaml` returns it parsed.
2. **JSON as well?** Not asked for. `--yaml` only, but the encoder layer should
   make `--json` a one-line addition if it's ever wanted.
3. **Should `swim status --yaml` keep printing the raw file**, or a
   schema-tagged rendering of it? Recommend the latter, with the file itself
   unchanged.

## Testing

- **Unit:** golden YAML for every event type; every document parses and has
  `schema`.
- **Go e2e:**
  - `swim run --yaml` on a pass/fail/skip DAG decodes into the expected event
    sequence: `start` before `finish` for each lane, and a lane's `start` after
    its parents' `finish`
  - the `summary` matches `status.yml`
- **Scenarios:** move `e2e/lib/common.sh` (`lane_results`, `step_output`) onto
  `--yaml` output, via `e2etool` for the parsing. Then layout changes to the
  human table can't break the suite.
