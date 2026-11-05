# Logs

```sh
swim log N            # a lane's latest round (--all: archives too; --follow: follow it until it ends)
swim wait N           # block until lane N's round ends; exit with its result
swim log <job|run>    # a job's rounds / one run
swim log              # .swim.log, the project log
swim log --since 1h   # what happened since: a duration, a UTC time, a run or a job
swim status [N]       # every lane's state
```

`swim log` renders colour, relative times and folded output (`… N more
times`; lines differing only in numbers never fold); `--full` expands,
`--raw` prints the file. The running step shows as `RUNNING <label> (in
progress)` with its output so far.

## The lane log

`.swim/logs/agentN.log`: append-only plain text, readable with `cat`. Each
line says what it is by how it starts:

```
# swim log | syntax 4 | swim N                       the log's syntax
== ROUND <utc>  job=<id>  <Round>                     a round starts
   run: <id> | script: lane.N.sh | owner: ... | git: <branch>@<sha> | env: ...
-- stage <name>  <hh:mm:ss>                           a stage starts
  PASS  <label>                <dur>s  <hh:mm:ss>     a step
  FAIL  <label> (exit 2)       <dur>s  <hh:mm:ss>     or (exit 130, interrupted), (timeout: ...), (exit 1, attempt 4/4)
        $ <command, quoted to re-run>
        -- attempt 1/4                                with --retry
        | <output, ANSI stripped>                     output is always fenced
        saved: <snapshot path>
        why: <the output line that says why>             a failed step's why (below)
  BLOCKED  <label> (matched "terraform destroy"; blocked_commands)    exit 87, counts as FAIL
  SKIP  <label> (dry run: <why>)                      a guard not approved
  APPROVED  <label> (<by whom>)
  STOP  <reason>
  WARN  <what>
== END <PASS|FAIL|SKIP|INTERRUPTED>  pass=P fail=F skip=S  exit=E  <dur>s  <utc>  run=<id>
   stages: snapshot PASS | check PASS | change SKIP | verify PASS
   failed: FAIL  <label> (exit <code>); ...
```

Results are the lines starting `  KIND  ` (`grep -E '^  (FAIL|BLOCKED)'`
finds every failure); anything indented 8 spaces belongs to the step above.

A failed step's `why:` is the line of its output that best says why,
picked once when it fails: the first line with (in this order) `--- FAIL:`
(joined with the `x_test.go:NN:` line below it: `--- FAIL: TestX:
x_test.go:41: got 1, want 2`), `panic:`, `FAIL:`, `fatal:`/`Fatal`,
`error:`/`Error:`/`ERROR`, or a `file.ext:NN:` location; else swim lint's
first `file:N  error` (or `warn`) finding, or a `needs gofmt` header with
the first file under it; else its last stderr line; else its last output
line. One line, secrets masked, cut at
160 characters; a step that printed nothing has none. The one-line views
show it after the step's result: `.swim.log`, `swim status`, bare `swim`,
`swim watch`, notifications, the run summary, `swim ci`'s job summary,
annotations and JUnit, and `failed_steps` in the `--yaml` documents.
The current round is everything after the last `== ROUND`. A lane that
exits before its round starts gets `STOP  not started: <why>`.
Logs written before the one syntax number start
`# swim lane log | syntax 2 | swim N`; swim reads them as they are.

## .swim.log

The project log, oldest first, one line per event; a new agent reads it
first. `swim note "<text>"` adds to it.

```
<utc>  <user>  <event>  [swim N]  [job=<id>]  [run=<id>]  <detail>
```

A `fail` line's detail ends with the failed steps, each with its why:
`... CI: unit tests  | FAIL  go test (unit) (exit 1): --- FAIL: TestX:
x_test.go:41: got 1, want 2`.

Events: `init`, `new`, `run`, `start`, `pass`/`fail`/`interrupted`, `skip`,
`approve`, `decline`, `run-done`, `archive`, `note`,
`usage` (a command swim refused as typed). Details summarize; the evidence
is in the lane log.

## status.yml

`.swim/status.yml` (`swim status`, `swim status --yaml`), one entry per
lane: `state` (idle, waiting, locked, queued, running, passed, failed,
skipped, interrupted; see [state](state.md)), `pending` and `pending_job`
(the script on disk), `round`, `job`, `run`, `script`, `log`,
`waiting_on`, `reason`, `locks`, `pid`, `started_at`, `finished_at`,
`duration_s`, `exit_code`, `pass`/`fail`/`skip`, `current_step`,
`failed_steps` (each with its why), `flag_approved`, `dry_run`,
`last_archive`.

## YAML

Versioned, for scripts and agents; prefer it to scraping text. Each
document carries `schema:`; fields are only added within a version.

| | |
|---|---|
| `swim run\|all --yaml` | `swim.run/v1`, one document per event as it happens: `run`, `waiting`, `locked`, `queued`, `start`, `output` (with `--yaml-output`), `step`, `finish`, `stop_requested`, `stopping`, `force_quit`, `kill`, then `summary` (result, exit, each lane's result). Same exit code. |
| `swim log N --yaml` | `swim.log/v1`: the round as data: `duration` as the log says it (`1m 5s`) and `duration_s` in seconds; results with label, stage, exit, `duration_s`, command, output, why; the running step as `RUNNING`. |
| `swim status --yaml` | `swim.status/v1`: status.yml, plus each pending lane's `pending_header` (after, guards and how each would be decided, timeout, locks, includes) and `deps`. |
| others | `swim.queue-list/v1`, `swim.verify/v1`, ...: [compatibility](compatibility.md#stability-tiers). |

Exit codes: `0` means everything selected passed; `3` means work waits on
an approval; `swim wait` exits with the result it waited for.
