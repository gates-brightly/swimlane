# swim report: what changed since you last looked

Status: proposed

## Summary

One read command that gives an agent the state of the work and everything
written since it last looked, untruncated, in one call:

- **`swim report`** prints a compact status, the `.swim.log` lines since the
  reader's earmark, and every lane-log line written since the earmark: the
  rest of the round that was in progress, then each round after it. It
  changes nothing.
- **`swim report --earmark`** prints the same report, then moves the earmark
  to the end of what it just printed. The next report starts there.

An earmark is a reader's place in the record. It is a cursor, not an edit:
swim never truncates, rotates or rewrites a log to make a report smaller.

```
$ swim report --earmark
swim report · swimlane · main@aa547f7 · since 14:02:11 (earmark planner-7f2c) · 6m ago

status   swim 1 PASS         Diamond root: fetch pages              (was running)
         swim 3 FAIL exit 1  Diamond child B: per-page stats        (was running)
         swim 4 waiting on 3                                        (new round)
         unchanged: swim 2 PASS
queue    1 waiting (#14)

.swim.log  4 new lines
2026-10-15T14:03:40Z  zach  pass   swim 1  job=51c7cf2e  pass=10 fail=0 ... 1.7s  Diamond root: ...
2026-10-15T14:04:02Z  zach  fail   swim 3  job=0acffc31  pass=1 fail=1 ... | FAIL  swim 1 outputs present (exit 1)
2026-10-15T14:05:10Z  zach  new    swim 4  job=db8e71f9  Diamond join: build report
2026-10-15T14:05:30Z  zach  queue  #14 swim 4

==> swim 1 · job=51c7cf2e · continues from line 41 of the round
  PASS  aggregate combined.html   0.4s  14:03:39
        $ ./aggregate.sh
        | wrote combined.html (14 pages, 212 KB)
-- stage verify  14:03:39
  ...
== END PASS  pass=10 fail=0 skip=0 drift=0  exit=0  1.7s  2026-10-15T14:03:40Z  run=r-0915

==> swim 3 · job=0acffc31 · continues from line 12 of the round
  FAIL  swim 1 outputs present (exit 1)   0.0s  14:04:02
  ...
== END FAIL  ...

earmark planner-7f2c moved to 14:08:17 (swim report --earmark)
```

## Motivation

- **Catching up costs more than the work.** Each cycle, a planner runs some
  mix of `tail -50 .swim.log`, `swim status`, `swim log N` per lane,
  `grep -E '^  (FAIL|BLOCKED)'`, `head`/`tail` on long outputs and `find
  .swim/logs`. Every call re-reads lines it has already seen, and the
  `head`/`tail` guesses either miss lines or pull in old ones. The brief's
  "Logs cost tokens" problem and the *log closes the loop* pillar
  (`swimlane.md`) are about exactly this.
- **Truncating to save tokens loses evidence.** The planner workflow says to
  validate real outcomes in step output, not PASS lines (guide.txt, PLANNER
  WORKFLOW step 5). `tail` drops the snapshot at the top of a round;
  `head` drops the verify at the bottom. A report that is small because it
  only holds *new* lines needs no truncation.
- **No command answers "what happened since I looked?"** `swim log`
  (`cmdLog`, `internal/cli/logcmd.go:28`) prints a whole log, a job's rounds,
  or a run's. `swim status` (`cmdStatus`, `internal/cli/status.go:21`) is the
  state now, with no notion of what changed. `.swim.log` is append-only and
  has no reader position. Between tasks or features, the agent has to
  reconstruct the boundary itself.
- **Rounds move under the reader.** An archive (`swim archive`, `swim clean`
  once [clean.md](clean.md) ships) renames `agentN.log` to
  `agentN.prev-*.log`. A byte offset into `agentN.log` taken before that is
  meaningless after it. The cursor has to follow rounds, not files.

## Behaviour

### What a report contains

In this order, each section omitted when it has nothing new:

| Section | Contents |
|---|---|
| Header | repo, branch@sha, the earmark's name and time, how long ago |
| Status | one line per lane whose state, round or job changed since the earmark, with what it was; then one `unchanged:` line naming the rest. Held locks, queue counts. Failed steps and `reason:` for changed lanes, as `swim status` prints them (`printStatus`, `internal/cli/status.go:89`). |
| `.swim.log` | every line appended since the earmark, verbatim |
| Lane logs | per lane (and per matrix cell / group job log), every line written since the earmark, verbatim, in lane order. A round the earmark was partway through continues from the next line; rounds started since are printed whole. |
| In flight | a running step's block from its spool (`inFlight`, `internal/cli/logcmd.go:311`), marked `RUNNING (in progress)`, exactly as `swim log N` adds it |
| Footer | where the earmark moved to, or `earmark not moved (peek)` |

- **Raw, not rendered.** Lines are the file's lines, as `swim log --raw`
  prints them. No folding, no colour, no relative times. The reader is an
  agent; folding would be the truncation this exists to avoid.
- **Nothing new is short.** `swim report · ... · nothing new since 14:02:11`
  plus the status summary line, and exit 0.

### Earmarks

- **Per reader.** An earmark has a name: `--name NAME`, else `SWIM_SESSION`
  (the same variable [lane-claims.md](lane-claims.md) uses), else `default`.
  Two sessions with different names each get their own "since". A worker
  that only cares about its lane narrows the report (below); the earmark
  still records every lane, so narrowing never makes the next unnarrowed
  report miss anything.
- **Moves only to what was printed.** The report takes its end positions
  *before* rendering (sizes of `.swim.log` and each lane log), prints up to
  them, then writes the earmark at them. Lines appended while it printed are
  in the next report, never skipped.
- **In-flight steps don't move it.** A running step isn't in the log file
  yet, so the earmark stays before it and the next report shows the step
  again, finished or still running.
- **No earmark yet.** The first report (or `--since`) starts at the oldest
  round still current in any lane's log (not archived), and `.swim.log` from
  that round's start. That's "the work in progress", which is what a new
  session needs; the whole archive history stays behind `swim log N --all`.
- **`--since T|RUN|JOB`** overrides the earmark for one report: a UTC time,
  a run id (from its `run` line in `.swim.log`), or a job (from its round
  start). `--since` with `--earmark` moves the earmark as usual.
- **`swim report --earmark --reset`** deletes the reader's earmark without
  printing. `swim report --earmarks` lists them (name, time, age).

### Narrowing

- `swim report 3 4` and `swim report <job>` report only those lanes' logs and
  status lines; `.swim.log` lines are filtered to those lanes (lines with no
  lane, like `note` and `run`, stay).
- `--no-logs` prints status and `.swim.log` only: the old `tail .swim.log;
  swim status` in one call, for a planner deciding whether to read further.
- `--results` prints the lane logs' structural lines only (`== ROUND`,
  `-- stage`, `  KIND  ` result lines, `== END` and its block) and drops
  step output (`        $` / `        |` lines), with a count per step and
  the command to see it (`swim log N`). Opt-in: the default is everything.

### Machine-readable

`swim report --yaml` emits `swim.report/v1`, built from the existing
documents rather than a new shape:

```yaml
schema: swim.report/v1
earmark: {name: planner-7f2c, since: 2026-10-15T14:02:11Z, moved_to: 2026-10-15T14:08:17Z}
status:
  changed: [{lane: 1, was: running, now: passed, round: ..., job: ..., failed_steps: []}]
  unchanged: [2]
  queue: {waiting: 1, running: 0, orphaned: 0}
history: ["2026-10-15T14:03:40Z  zach  pass  swim 1 ..."]   # lines, verbatim
lanes:
  - lane: 1
    cell: ""
    rounds:
      - continues_from: 41          # 0 for a round started since the earmark
        round: { ...swim.log/v1 document, results since the earmark only... }
```

Text is the default because it is cheaper per token for the same content;
YAML is for harnesses that want to branch on `status.changed`.

### Exit codes

- `0`: report printed (with or without new content).
- `swim report --wait [--timeout D]` blocks until there is something new
  since the earmark, then reports. It polls like `swim wait`
  (`cmdWait`, `internal/cli/queue.go:306`) and exits as `swim wait` does on
  timeout. With `--earmark` this is the agent loop in one command:
  `swim report --earmark --wait`.

## Design

### The cursor

```yaml
# .swim/earmarks/planner-7f2c.yml   (git-ignored, like the rest of .swim/)
schema: swim.earmark/v1
name: planner-7f2c
at: 2026-10-15T14:08:17Z
history: {offset: 48213, sha: 9f1c...}     # bytes of .swim.log consumed; sha of the last line
lanes:
  "1": {round: "2026-10-15T14:03:20Z job=51c7cf2e-...", lines: 58, sha: 77ab..., state: passed, job: 51c7cf2e-...}
  "3": {round: "2026-10-15T14:04:01Z job=0acffc31-...", lines: 19, sha: 0c3e..., state: failed, job: 0acffc31-...}
  "3[orders]": {round: ..., lines: ..., sha: ...}
```

- **Lanes are tracked by round, not by file.** `round` is the round's `==
  ROUND <utc> job=<id>` line (unique per round); `lines` is how many of the
  round's lines were consumed; `sha` is the hash of the last consumed line.
  To resume, gather lane N's rounds from its archives and current log
  (`archives` + `splitRounds`, `internal/cli/logcmd.go:195` and `:258`),
  find the round whose first line matches, check `sha` against line
  `lines-1`, and print from `lines` on, then every later round. This
  survives an archive, a `clean`, and a `swim log` syntax migration that
  leaves round lines alone.
- **Fallbacks are said, not silent.** If the round can't be found (pruned)
  or `sha` doesn't match (log rewritten), the report prints every round of
  that lane that started after the earmark's `at`, with a `WARN  earmark lost
  its place in swim N's log (<why>); showing rounds since <at>` line.
- **`.swim.log` by offset.** It is append-only (`history.Append`,
  `internal/history/history.go:93`), so a byte offset is safe. The `sha`
  check catches a hand-edited or replaced file; the fallback is lines with a
  timestamp after `at`.
- **Status by snapshot.** `state` and `job` per lane are what the
  status diff compares against. They come from `status.yml` at report time,
  after the same `refreshStatus` that `swim status` runs.
- **Written atomically** with `fsutil.WriteAtomic`
  (`internal/fsutil/fsutil.go:27`). Two reports under one name racing is
  last-writer-wins; both printed a correct report, and the later earmark is
  at or past the earlier.

### Where it lands

| Area | Change |
|---|---|
| `internal/earmark` (new) | `Cursor` type; `Load(root, name)`, `Save`, `List`, `Remove`; `Name()` resolving `--name`/`SWIM_SESSION`/`default`. No knowledge of logs. |
| `internal/lane` (or `internal/logparse`) | Move `splitRounds`, `archives` and `firstRoundTime` out of `internal/cli/logcmd.go` so `swim log` and `swim report` share them. Add `Rounds(root, n) []RoundRef` (file, header line, lines) across archives and the current log, cells included. |
| `internal/history` | `ReadFrom(root, offset) (lines []string, end int64, err error)` and `LastLineSHA`. |
| `internal/cli/report.go` (new) | `cmdReport`: resolve the earmark (or `--since`), snapshot end positions, build the report (status diff, history lines, lane deltas, in-flight spools), render text or `swim.report/v1`, then save the earmark when `--earmark`. `--wait` loops on "anything past the cursor?" using the same end-position snapshot, so it's cheap (stat calls, no parsing). |
| `internal/cli/status.go` | Factor the per-lane line out of `printStatus` (`:89`) into `statusLine(l, state, p)` so the report's changed-lane lines match `swim status` exactly. |
| `internal/logparse` | `Round.DocSince(n, line)`: a `swim.log/v1` document holding only results at or after line `n`, for the YAML form. |
| `internal/cli/cli.go` | Register `report` next to `status` (`:38`). |
| `internal/help/help.go` | `"report"` command help next to `"status"` (`:256`) and `"log"` (`:428`); add it to `FullOrder` (`:1341`) after `log`. |
| `internal/help/guide.txt` | PLANNER WORKFLOW step 0 (`:231`) becomes `swim report --earmark` (with `.swim.log`/`swim status` as what it's made of); step 5 (`:252`) reads the round with `swim report --earmark` and drops to `swim log N` only for older rounds. Add `swim.report/v1` to the YAML list. Add `.swim/earmarks/` to the files list. |
| README, CHANGELOG | Document under reading results; one `Added` line under Unreleased. |

### Implementation steps

1. **Share the round plumbing.** Move `splitRounds`/`archives`/
   `firstRoundTime`/`inFlight` to a package `swim log` and `swim report` can
   both use; `cmdLog` calls the moved versions. No behaviour change; the
   existing log tests must pass untouched.
2. **`internal/earmark`.** Cursor type, YAML load/save (atomic), list,
   remove, name resolution. Unit tests for round-trip and name precedence.
3. **`history.ReadFrom`.** Offset read with the last-line SHA check and the
   timestamp fallback. Unit tests including a truncated and a replaced file.
4. **Lane deltas.** `laneDelta(root, n, cursor) ([]RoundSlice, warn string)`:
   find the cursor round across archives and the current log, verify `sha`,
   slice from `lines`, append later rounds. Same for each cell log of a
   matrix or group lane. This is the core; test it hardest (below).
5. **Status diff.** Compare `status.yml` lanes (after `refreshStatus`)
   against the cursor's per-lane `state`/`job`; reuse the factored
   `statusLine`.
6. **`cmdReport` text output** with `--earmark`, `--name`, `--since`,
   narrowing by lane/job, `--no-logs`, `--results`. Snapshot end positions
   first; save the earmark last, only after output was written without error.
7. **`--yaml`** (`swim.report/v1`) via `Round.DocSince`.
8. **`--wait [--timeout D]`**, `--earmarks`, `--reset`.
9. **Docs**: help text, guide.txt planner workflow and YAML list, README,
   CHANGELOG.
10. **Dogfood**: switch this repo's own agent instructions (swim's CI lanes
    and any CLAUDE/agent docs that say `tail .swim.log`) to `swim report
    --earmark`, and compare the bytes a planner reads per cycle before and
    after on a real multi-lane round.

## Open questions

- **Should `--earmark` write a `.swim.log` line?** (`earmark planner-7f2c`.)
  It would show in history when each session last caught up, which helps a
  person reading `.swim.log`. But reading isn't a top-level action, and an
  agent looping `--earmark --wait` would fill `.swim.log` with them. Leaning
  no; `swim report --earmarks` answers "who looked when".
- **Default detail level.** This spec defaults to everything new, verbatim,
  with `--results` as opt-in. If real rounds with chatty steps (`terraform
  plan`) make that too large, the default could be full output for non-PASS
  steps and `--results`-style for PASS steps. Decide after step 10's
  measurement, not before.
- **A per-report size ceiling?** A runaway step can write megabytes. Should
  the report stop at a byte budget (`--max-bytes`, default unlimited), print
  where it stopped, and move the earmark only that far, so the next report
  continues? That keeps "never truncated" (nothing is dropped, only
  deferred) while bounding one call.
- **Hub.** Should the hub serve a report per hub user, with the earmark kept
  hub-side? Out of scope for now (below), but the cursor format shouldn't
  preclude it.
- **Exit code for `--wait` with nothing new at timeout:** match `swim wait`
  exactly, or a distinct code so a loop can tell "timed out quietly" from
  "error"?

## Testing

- **Unit, lane deltas** (`laneDelta`), each against a fixture log:
  - cursor mid-round in the current log → the rest of that round only;
  - cursor mid-round, then the lane was archived and a new round started →
    the rest from the archive, then the new round whole;
  - cursor at the end of a finished round, nothing since → nothing;
  - cursor round pruned → rounds since `at`, with the WARN;
  - cursor `sha` mismatch (log rewritten) → same fallback, different why;
  - matrix lane: each cell log tracked separately.
- **Unit, history**: offset read; appended while reading (end position
  snapshot holds); file replaced → timestamp fallback.
- **e2e, the loop** (`internal/e2e`): run two lanes, `swim report
  --earmark` shows both rounds whole; run lane 1 again, `swim report` shows
  only lane 1's new round and the `.swim.log` lines for it, and lane 2 under
  `unchanged:`; a second `swim report` (no `--earmark`) prints the same
  thing; after `swim report --earmark`, the next report is `nothing new`.
- **e2e, never skips**: append to a lane log between the end-position
  snapshot and the save (failpoint, `internal/failpoint`); the next report
  contains the appended lines.
- **e2e, in flight**: with a lane running a long step, `swim report
  --earmark` shows the RUNNING block; after it finishes, the next report
  shows the finished step's block (not skipped because it was seen running).
- **e2e, two readers**: `--name a` and `--name b` move independently.
- **e2e, `--wait`**: returns as soon as a lane writes a line; times out with
  `swim wait`'s exit code when nothing happens.
- **Read-only**: `swim report` without `--earmark` changes nothing under
  `.swim/` (hash the tree before and after), as clean.md's dry-run test does.
- **YAML**: `swim.report/v1` validates, and each `round` document is a valid
  `swim.log/v1` result subset.

## Out of scope

- Truncating, rotating or compacting logs. The report is small because it
  is a delta, not because anything was cut.
- Summarising output (LLM or heuristic). The report selects lines; it never
  rewrites them.
- Hub-side earmarks and reports.
- Earmarks across repos or worktrees.
