# Retry on a match: wait out another tool's lock, with a stage or lane default

Status: dropped (2026-10-15). Deciding retries by matching a step's output
against regexes isn't wanted: it's fragile, and it puts a step's meaning in
patterns over text. `--retry N`, `--backoff` and `--retry-on CODES` stay as
they are. Kept for the record; not to be built.

## Summary

swim already retries a step: `--retry N --backoff D` tries up to N more
times, waiting D, then 2D, 4D, ... (capped at 5m, ±20% jitter), and
`--retry-on CODES` limits it to some exit codes. That's not enough for a
tool like Rush, which holds a repo-wide lock while it builds and fails a
second command with exit 1, the same code as a real build failure.

This adds:

- **`--retry-if REGEX`**: retry only when the attempt's output matches. A
  build that's broken fails at once; a build that found the lock waits and
  tries again.
- **`--backoff-max D`** and **`--retry-for D`**: a cap on each wait, and a
  budget for all of them, instead of the fixed 5m.
- **Named policies** in `swim.yml`, and **defaults** for a stage or a whole
  lane, so a Rush repo says it once.

```yaml
# swim.yml
retry:
  rush-lock:
    if: ["Another Rush command is already running", "rush.*\\.lock"]
    max: 8                 # extra attempts
    backoff: 2s            # 2s, 4s, 8s, 16s, 32s, 60s, 60s, 60s
    backoff_max: 60s
    for: 10m               # and never more than 10m of retrying in all
```

```bash
# Retry: rush-lock                         # every step in the lane, unless it says otherwise
...
stage check
run "rush build" rush build -t orders-api  # retried only while Rush is locked
stage change --retry rush-lock             # this stage's steps; overrides the header
run "rush publish" rush publish --apply
run --retry none "tf apply" terraform apply   # this step never retries
```

```
  FAIL  rush build (exit 1)                                    41.3s  14:02:51
        $ rush build -t orders-api
        -- attempt 1/9
        | Another Rush command is already running in this repository.
        -- exit 1 (0.4s); matched "Another Rush command is already running"; retry in 2.1s
        -- attempt 2/9
        ...
        -- attempt 4/9
        | rush build (12 projects) ... done
        -- exit 0 (38.6s)
  PASS  rush build                                             41.3s  14:02:51
```

## Motivation

- **Rush's lock isn't swim's.** `# Locks:` keeps swim's own lanes from
  overlapping. It can't see a `rush build` someone started in another
  terminal, an IDE's background build, or a CI agent sharing the checkout.
  Rush fails the second command, and the round fails with it.
- **Exit codes can't tell the cases apart.** A lock failure and a compile
  error are both exit 1. `--retry 5` would retry real failures five
  times, slowly, and on a change step it would rerun something that may
  have half happened. The guide already says to put `--retry` only on
  idempotent steps for that reason.
- **A lock failure happens before anything changes.** Rush refuses to start,
  so retrying a lock failure is safe even on a step in `stage change`,
  provided the retry fires only on the lock message. That's what
  `--retry-if` makes possible.
- **The same rule repeats everywhere.** Every Rush step in every lane needs
  the same pattern and timing. A named policy, and a lane or stage default,
  keeps the scripts short and the rule in one reviewed place.

## Behaviour

### New step options

On `run`, `gate`, `snapshot`, `evidence`, the in-lane `swim run` form, and
SDK `opts`:

| Option | Means |
|---|---|
| `--retry-if REGEX` | Retry only when the attempt's output matches REGEX (Go `regexp`, case-sensitive; `(?i)` for not). Repeatable: any match retries. Checked against the attempt's output after masking, stdout and stderr together. |
| `--backoff-max D` | Cap each wait at D (default 5m, as today). |
| `--retry-for D` | Stop retrying once D has passed since the first attempt began: no new attempt starts after it. With `--retry-for` and no `--retry N`, the count is unlimited and the budget decides. |
| `--retry POLICY` | Use a named policy from `swim.yml` (below). `--retry N` keeps its meaning; `--retry none` turns off a default. |

- **Matching is on top of exit codes, not instead.** An attempt is
  retried when it failed (non-zero exit), its code is allowed (`--retry-on`,
  default any), and, with `--retry-if`, its output matches. A passing
  attempt is never retried.
- **The match goes in the log.** The attempt's note says which pattern
  matched (`matched "Another Rush command..."`). A failure that matches
  nothing says `no retry: output didn't match --retry-if`, so a reader sees
  at once why a broken build failed fast.
- **Only the last 64 KB of an attempt's output** is searched, so a huge
  build log doesn't make matching slow. Lock messages come early or last,
  and both fit.
- **The schedule** is the existing one: the first wait is `--backoff`
  (default 2s), each next one doubles, capped at `--backoff-max`, each
  ±20% jittered so parallel lanes don't retry in step. With `--backoff 2s
  --backoff-max 32s`: 2, 4, 8, 16, 32, 32, 32...
- **Limits still win.** The round's `Timeout:` stops retrying, as today. A
  step limit (`--timeout`) applies per attempt. `--retry-for` bounds the
  retrying. An operator stop or Ctrl-C ends the wait at once, as today.

### Named policies

```yaml
# swim.yml (committed; part of the policy hash)
retry:
  rush-lock:
    if: ["Another Rush command is already running"]   # --retry-if, any of
    on: [1]                                             # --retry-on (optional)
    max: 8                                              # --retry N (optional with for)
    backoff: 2s
    backoff_max: 60s
    for: 10m
    timeout_retries: false                              # --no-retry-timeout's inverse; default false here
```

- **`--retry rush-lock`** on a step uses it. An option on the step wins over
  the same field of the policy (`--retry rush-lock --retry-for 2m`).
- **In `swim.yml`, so it's reviewed and pinned.** Policies are part of the
  policy hash, like `blocked_commands`: changing one stales approvals of
  lanes that use it, because how a step retries is part of what was
  approved. An unknown policy name is a lint error, and a lane naming one
  doesn't start.
- **`swim.yml` may also set `retry_default: rush-lock`**: the policy for
  every step of every lane that names none. It's off unless set, and meant
  for repos where one tool's lock is everywhere.

### Defaults for a lane and a stage

- **`# Retry: POLICY`** in the header: the default for every step in the
  lane (`retry:` in `lane.N.yml`). `# Retry: none` turns off a repo
  default for the lane.
- **`stage NAME --retry POLICY`**: the default for the steps of that stage,
  until the next `stage`. `stage change --retry none` turns it off for a
  stage. A stage default overrides the lane's, and a step's own options
  override both.
- **Handlers and finally lanes** take defaults like any steps. Their
  steps run soft anyway.
- **Precedence**, highest first: the step's options, the stage's `--retry`,
  the header's `Retry:`, `swim.yml` `retry_default`, and none.
- **The log says where a retry rule came from**: the step block's first
  attempt note reads `-- attempt 1/9 (retry: rush-lock, from stage change)`.

### Safety

- **`--retry-if` is what makes retrying a change step reasonable.** `swim
  lint` already warns about `--retry` in `stage change` (only idempotent
  steps should retry). With `--retry-if`, the warning becomes a note when
  every pattern looks like a refusal to start (lock, busy, in use, already
  running); otherwise it stays a warning. A policy may declare
  `refusal: true` to say so, and lint takes its word for it.
- **A guard's approval holds across retries.** A guarded step that waits
  out a lock is still one approved step: the guard asked once, before the
  first attempt. Its `Observe:` probes are read again before each attempt,
  so a world that changed during a long wait ends the round as drift.
- **Retries never hide failures.** The step counts once, PASS or FAIL. The
  END block records `retried: rush build 3x (41s waiting)`, and
  [observe.md](observe.md)'s `retried` and `flaky` observations count them,
  so a lock that's always held shows up as a fact instead of a slow round.

### Versus waiting for the lock

Another approach is to wait for the lock to go away, rather than fail and
retry: `--wait-while 'test -e common/temp/rush.lock'`, or a `wait_for`
module ([modules.md](modules.md)). Retrying on the tool's own message is
proposed first because:

- it needs no knowledge of where the tool keeps its lock, or how it names
  it, which changes between versions;
- it handles the race where the lock appears between the check and the
  command;
- it works for any tool that refuses with a message: Terraform's state lock
  (`Error acquiring the state lock`), apt's `Could not get lock`, Gradle's
  `Timeout waiting to lock`, git's `index.lock`.

A `wait_for` module can still be the right tool when a lock is known and
long-held.

## Design

| Area | Change |
|---|---|
| `internal/step/step.go` | `Retry` gains `If []*regexp.Regexp`, `BackoffMax`, `For`, and `Source` (for the log note). The retry loop checks the match against the attempt's last 64 KB (a ring buffer beside the spool), uses `BackoffMax` instead of the 5m constant, and stops at `For`. Notes say what matched or why it didn't retry. |
| `internal/cli/commands.go` (`cmdStep`) | Parse `--retry-if`, `--backoff-max`, `--retry-for`, `--retry POLICY|none`; resolve the effective rule (step, then the stage default from `SWIM_RETRY_STAGE`, then `SWIM_RETRY_LANE`, then `retry_default`). |
| `internal/config` | `swim.yml` `retry:` policies and `retry_default`, validated (patterns compile, durations parse), in the policy hash (`policy.go`, PolicyVersion bump). |
| `internal/lane` | `# Retry:` in every runtime and `retry:` in the sidecar. |
| `internal/assets/lib.sh`, `swim stage` | `stage NAME --retry POLICY` exports `SWIM_RETRY_STAGE` until the next stage; `lane_init` exports `SWIM_RETRY_LANE` from the header. |
| `internal/cli/lanesock.go`, `proto/swim/lane/v1` | The new options pass through `opts`, as `--retry` does today; `stage` takes `retry`. |
| `internal/logparse` | The END block's `retried:` line (additive, so no log syntax bump); `swim log --yaml` gains `attempts`, `retried_for_s`, `retry_rule` on results. |
| `internal/lint` | Unknown policy (error); `--retry-if` that doesn't compile (error); the change-stage warning becomes a note for refusal patterns; `--retry` with neither a count nor `--retry-for` (error). |
| `internal/launcher/plan.go` | Each lane's retry rules (header, stages), for the approver. |
| `internal/help`, README | The new options next to `--retry`; a RETRYING paragraph with the Rush example; `swim.yml` `retry:`. |

### Implementation steps

1. **The step loop:** `--retry-if` against a 64 KB output tail,
   `--backoff-max`, `--retry-for`, the notes. Unit tests with a fake
   command that fails with a message N times, then passes; one that fails
   without it; budget and cap timing with a fake clock.
2. **Options in `swim step` and SDK opts**, and `swim run "<label>" --`.
3. **`swim.yml` policies:** parsing, validation, policy hash, `--retry
   POLICY`.
4. **Defaults:** `# Retry:`, `stage --retry`, `retry_default`, precedence,
   and the note naming the source.
5. **Lint, plan, the END block's `retried:` line, `--yaml` fields.**
6. **e2e, the Rush case:** a stand-in `rush` that refuses while a lock file
   exists and a background step that removes it after 3s. The step waits
   it out and passes; with a compile error instead, it fails on the first
   attempt.
7. **Docs.**

## Open questions

- **The schedule you asked for.** 2, 4, 8, 16, 24, 32 doubles to 16 and then
  grows by 8. Pure doubling with a cap (`--backoff 2s --backoff-max 32s`:
  2, 4, 8, 16, 32, 32...) is what this spec proposes, since it's what
  `--backoff` already does. Was the 24 intentional? If so, a `--backoff-step
  D` (add D once the cap is reached, up to a ceiling) could express it.
- **Match on output or exit code first?** Some tools exit with a dedicated
  code when locked (apt: 100; Terraform with `-lock-timeout` waits itself).
  For those, `--retry-on` alone is enough. Keep `--retry-if` for tools like
  Rush that don't.
- **Should a policy be able to wait instead,** using a tool's own lock
  timeout when it has one (`terraform -lock-timeout=5m`)? That's better
  when it exists; the policy could carry a note, and lint could suggest it.
- **Retry budget across lanes.** Four lanes all waiting on one Rush lock
  retry independently. Should swim notice they're waiting on the same
  thing (the same policy and match) and say so in the live view, or even
  queue them? Leaning: say so; don't queue.

## Testing

- **Unit, the loop:** retried only on a match; not on a non-match; not on
  a pass; the count, `--retry-for` and `--backoff-max` each stop it; the
  round's `Timeout:` still wins; Ctrl-C during a wait ends it; the notes
  name the match.
- **Unit, matching:** only the last 64 KB is searched; masking runs before
  matching (a pattern can't match a secret's value); `(?i)` works.
- **Unit, precedence:** step over stage over header over `retry_default`;
  `none` at each level.
- **e2e, the Rush case** (step 6 above), on a bash lane and a Go SDK lane.
- **e2e, policy:** an unknown `--retry` name refuses the lane and is a lint
  error; changing the policy in `swim.yml` stales a signed approval.
- **e2e, change stage:** a guarded `stage change` step with a refusal policy
  is asked once and retried without asking again.

## Out of scope

- Retrying a whole round or stage (rerunning earlier steps). A retry is one
  step's attempts.
- Detecting locks by inspecting other processes or lock files. That's a
  module's job, if it's anyone's.
- Changing the default backoff or cap for existing `--retry` users.
