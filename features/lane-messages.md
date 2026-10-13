# Lane messages (`msg`)

Status: shipped (unreleased)

## Summary

A running round can post a short, changing status message that says where
it is inside a long job. The message shows in the live view, `swim status`,
`swim watch` and the hub, beside the lane's current step:

```bash
stage change
for svc in $SERVICES; do
  msg --progress "$i/$total" "migrating $svc"
  run "migrate $svc" ./migrate.sh "$svc"
done
```

```
 swim 3  / running 12:04  Cut over orders-api   migrating billing (7/25)
```

Commands inside a step can post too, so a 40-minute `run` can report what
it's doing from the inside:

```python
subprocess.run(["swim", "msg", "--progress", f"{n}/{total}", f"backfilled {table}"])
```

## Motivation

- **Long steps look frozen.** A step like `run "backfill" ./backfill.py`
  shows `running 38:12` and nothing else until it ends. The operator can't
  tell slow from stuck without opening the log, and the hub can't tell them
  at all.
- **Step labels are fixed.** The label says what a step is, not how far
  along it is. Splitting a job into hundreds of steps just to show progress
  bloats the log.
- **Unattended runs are the main case.** `swim watch` and the hub exist for
  rounds nobody is watching. A one-line "where is it now" is the most useful
  thing a card can show.

## Behaviour

### Posting

- **Bash:** `msg [--progress N/M] "<text>"`, a library function.
- **Any language, and commands inside a step:** `swim msg [--progress N/M]
  "<text>"`. It reads `SWIM_LANE`, which every lane and every step's process
  already has.
- **SDK:** `r.msg(text, progress=(n, m))`, with
  [native-sdk.md](native-sdk.md).
- **Clearing:** `msg ""` clears the message. The message is also cleared
  when the round ends, so it never outlives its round.
- **Latest wins:** each lane has one message.
- **Never fails the round:** a `msg` that can't be written (no lane, a
  read-only disk) prints a warning and returns 0.

### What's shown

| Where | Shows |
|---|---|
| Live view panel | the message after the round, truncated to fit; `(7/25)` from `--progress` |
| `swim status` / `status.yml` | `message`, `progress`, `message_at` |
| `swim status --yaml` | the same fields (`swim.status/v1`, additive) |
| `swim run --yaml` | a `msg` event per update |
| `swim watch` | the running lane's message in its activity line |
| Hub card and worker page | the message and a progress bar; `GET /api/workers` gains `message` and `progress` |

- **Plain output** (no terminal, `--plain`): messages print as
  `[3] … migrating billing (7/25)`.
- **Hub stale hint:** a message with no update for a long time is shown with
  its age (`migrating billing · 9m ago`), which helps spot a stuck job.

### Text rules

- **Clean text:** one line, with control characters and ANSI removed,
  capped at 120 characters.
- **Masked** through the same masker as everything swim writes.
- **Can't fake state:** the message always shows in its own column or
  style, so a message like `PASS` can't pass for a lane state.

### The log

- **Recorded:** messages go into the lane log as `  MSG   <text> (7/25)
  12:04:10` lines, so a planner reading the log back sees the job's
  milestones.
- **Throttled:** an identical message is dropped. Otherwise at most one
  `MSG` line is written per `msg_log_interval` (default `10s`) per lane, and
  the latest message is always written when a step ends.
- **`swim log`** shows `MSG` lines dimmed. `swim log --yaml` (`swim.log/v1`)
  adds them as `messages` on the step they happened in.

## Design

- **Storage:** `.swim/msg/laneN`, a tiny file of `text`, `progress` and
  `at`, written atomically (write a temp file, then rename). The step's
  process, the lane script and the launcher never fight over `status.yml`.
- **Command:** `swim msg` beside the in-lane commands
  (`internal/cli/lanecmds.go`). The bash library's `msg` calls it.
- **Launcher and display:** the panel's refresh tick reads
  `.swim/msg/laneN` for running lanes. `display.LaneView` gains `Message`
  and `Progress`. The `msg` YAML event is emitted when the file changes.
- **Status:** `status.Lane` gains `Message`, `Progress` and `MessageAt`,
  filled from the file when status is written or read. They're cleared by
  `_finish`.
- **Log:** `_finish` and the step end write the throttled `MSG` lines.
  `internal/logparse` learns the `MSG` kind.
- **Hub:** `hub.Lane` gains `Message` and `Progress`, set where the worker
  builds its report (`internal/cli/hub.go:303`). The pages render a bar when
  progress is set.
- **Config:** `msg_log_interval` (a duration; `0` logs every change).

## Open questions (answered)

1. **Name:** `msg`. It's short, doesn't collide with `swim status`, and
   reads naturally in a loop.
2. **Log or not:** log, throttled. A planner reading the log back gets the
   job's milestones, and throttling keeps the token cost small.
3. **Progress format:** `N/M` only. A percentage is derived where it's shown
   (the hub's bar). An ETA can come later.
4. **Scope:** a message stays until the next `msg` or the round's end, so a
   loop's message survives across the steps in it.
5. **Notifications:** a separate feature. `msg` never notifies.

Decided while building:

- **Writes never interleave:** a message posted during a step is queued in
  `.swim/msg/laneN` and written into the step's block (`-- MSG  ...`) when
  the step ends, so nothing writes into a running step's output.
- **Printing:** `swim msg` prints nothing, since inside a step that would
  land in the step's output. The launcher prints `[N] … text` when the
  message changes, polling every 200ms, so fast updates coalesce.
- **Status** is written by `swim msg` itself, under status.yml's lock.

## Testing

- **Posting:** from the lane script, from a command inside a `run`, and
  from a non-bash lane. Each shows in status, the panel and `--yaml` within
  one refresh.
- **Lifetime:** latest wins, `msg ""` clears, and the round's end clears.
  No message leaks into the next round.
- **Text:** ANSI and newlines stripped, length capped, secrets masked.
- **Log throttling:** duplicates dropped, at most one line per interval,
  the last message logged at step end. `msg_log_interval: 0` logs every
  change.
- **Never fails a round:** `msg` outside a lane, or with an unwritable
  `.swim/msg`, warns and exits 0.
- **Hub:** the message and progress appear on the card and in
  `/api/workers`. An old message shows its age.
- **bash 3.2:** the library function runs under macOS's bash.

## Out of scope

- **Several messages per lane** or a message history view. The log keeps
  the history.
- **Structured progress events** for other tools beyond the `msg` YAML
  event.
- **Messages from outside a round** (from the planner, or from another
  lane).
