# Changelog

What changed in swim, newest first. Each `##` section is one revision.
`swim changelog` prints this from the binary you are running:

```sh
swim changelog                  # the newest revision (what you just updated to)
swim changelog -n 3             # the newest three revisions
swim changelog --since 2.20261009   # everything after the version you came from
swim changelog --all            # every revision
```

Versions are `<breaking>.<YYYYMMDD>` (`swim --version`), and release tags are
`v0.<breaking>.<YYYYMMDD>`. A new breaking number means lane scripts, logs or
swim's state changed incompatibly. Read that revision's **Upgrading** notes and
`swim lock --help` before running lanes. Revisions before versioning existed are
named by commit.

There is one release per breaking number per day. A second release on the
same day bumps the breaking number (`Breaking` in `internal/version`) and gets
**Upgrading** notes like any other breaking release.

When you cut a release, rename `## Unreleased` to `## <version> (v0.<breaking>.<date>, <commit>, <date>)`
and start a new empty `## Unreleased` above it.

## Unreleased

Everything after the `v0.2.20261009` tag on `main` (`3b3528d` … `47da625`). To
use it before the next release, build from a checkout (`make link`).

### Upgrading

- **Lane scripts and logs move to syntax 2.** This happens automatically the
  first time any swim command loads the repo. Originals are copied to
  `.swim/migrations/<time>/`, and the migration is recorded in `.swim.log`. A
  running lane's files are migrated after it finishes. The breaking version
  stays 2, so `swim lock --upgrade` isn't needed.
- **A file written in a newer syntax stops swim** with a prompt to update.

### Added

- **Syntax 2 for lane scripts and logs:**
  - **Header metadata:** `# swim: syntax 2`, `Owner:`, `Created:`, `Guards:`
    (one line per flag) and `Timeout:`. Other `# Key: value` header lines, e.g.
    `# Ticket: OPS-42`, are kept and shown in the round header.
  - **Stages:** `stage snapshot|check|change|verify`. An out-of-order stage is
    recorded as WARN; an unknown stage stops the round. Each stage's result is
    recorded in the log and `status.yml`.
  - **Round time limit:** `# Timeout: 30m`. A step still running at the limit
    is stopped (FAIL, timeout) and the round ends.
  - **Log layout:** lane logs are grouped by round and stage, with step output
    fenced by `| `.
- **Run id:** every `swim run` / `swim all` gets one, e.g.
  `r-20261009T141118Z-7c2e` (override with `--run-id`).
  - It's exported to lanes as `$SWIM_RUN`, with the run's lanes in
    `$SWIM_RUN_LANES`.
  - It's recorded in the round header, `status.yml` and every `.swim.log`
    event.
  - `swim status --run RUN` and `swim log RUN` show one run.
- **Parallel limit:** `max_parallel` in config, or `--parallel N`.
  - Lanes that are ready but over the limit wait as `queued (#n)`.
  - When a slot frees, it goes to the lane with the longest chain of work
    still waiting on it.
  - Queued lanes that a Ctrl-C stops count as skipped.
- **Resource locks:** `# Locks: name[, name ...]`.
  - Lanes that share a lock never run at the same time, but neither has to
    pass first, and a failure doesn't skip the other.
  - Locks hold across concurrent `swim run`s in one repo, and are released
    when the lane's process ends.
  - Waiting lanes show `locked (name: swim N)`. `swim status` lists the locks
    being held, and `swim plan` shows which lanes take turns on which lock.
- **Per-step timeouts and retries:** `run`/`gate`/`snapshot` accept
  `--timeout D`, `--retry N`, `--backoff D` and `--retry-on CODES`, and
  `# Step-Timeout:` sets a default for every step.
  - Each attempt is logged separately.
  - A step never runs past the round's `Timeout:`.
  - Guards, confirms and blocked steps are never retried.
- **Blocked commands:** swim never pushes, commits or pulls.
  - A lane whose script or step command contains `git push`, `git commit` or
    `git pull` is stopped and recorded as `BLOCKED`, with exit 87, before or
    at that step.
  - A `git` shim on each lane's PATH also catches forms like `git -C dir push`.
  - Repos can add patterns with `blocked_commands`; the built-in three can't
    be removed.
  - Swim's own code only reads from git, and a test enforces that.
- **Secret masking:** the values of `secret_env` variables, of auto-detected
  names (`*_TOKEN`, `*_SECRET`, `*_PASSWORD`, `*_API_KEY`, …) and of well-known
  credential formats become `***` in everything swim writes: logs, snapshots,
  `status.yml`, `.swim.log`, notes and the terminal output. Base64 and
  URL-encoded forms are caught too.
- **`swim timeline [RUN|N|JOB]`:** when each lane of a run waited, queued, was
  locked, ran and finished; how long each lane took to start after its last
  parent finished; and the longest dependency chain against the run's total
  time.
  - `--top N`, `--all` and `--steps` control what's shown.
  - `--yaml` and `--html FILE` give the same data for scripts and for sharing.
- **`--yaml` output** for `run`, `all`, `plan`, `log`, `status`, `timeline`,
  `lint` and `doctor`, with versioned schemas (`swim.run/v1`, …).
  - `swim run --yaml` writes one YAML document per event, then a summary.
  - `--yaml-output` adds each lane's output lines.
- **`swim lint [N|JOB ...]`:** checks lane scripts, and changes nothing. It
  looks at headers, `set -e`, `lane_init` and `summary`, stages, bash 3.2
  compatibility, guard flags, blocked commands, secrets, and lanes that read
  another lane's output without depending on it.
- **`swim doctor [--fix]`:** checks the environment.
  - It looks at the binary's platform, other `swim` binaries on PATH, config,
    `.gitignore`, stale pid files, the toolchain, git and `.swim.lock`.
  - `--fix` makes safe local repairs only.
- **Chime when a run finishes:** set with `swim config chime true|false|failure`.
  - `chime_style` is `bell`, `sound` or `notify`. `chime_min_s` (default 10s)
    keeps short runs quiet.
  - On failure the bell rings twice.
  - It never chimes in CI or when output isn't a terminal, and it never
    changes the exit code.
- **`swim config KEY [VALUE] [--repo]`** gets and sets `lanes`, `chime`,
  `chime_style` and `chime_min_s`. `--lanes N` still works.
- **`features/`:** specs for planned and shipped features, with their status.

### Changed

- **The pre-start state is now called `starting`.** `queued` now means
  "waiting for a parallel slot".
- **`BLOCKED` is a new result kind.** It counts as a failure in pass/fail
  counts, `failed_steps` and stage results.

## 2.20261009 (v0.2.20261009, c6a87b0, 2026-10-09)

First published release. Install it with
`go install github.com/gates-brightly/swimlane/cmd/swim@v0.2.20261009`.

### Upgrading

- **From breaking version 1:** run `swim lock --upgrade`. Lane scripts are
  unchanged.

### Changed

- **Breaking version 2.** `.swim/status.yml` now records the round a skipped
  lane skipped (job, round and `finished_at`). `swim plan` uses it to show that
  lane as a retry.
- **Release tags are `v0.<breaking>.<YYYYMMDD>`.** Go needs a `/vN` module path
  for majors above 1, so swim's breaking number goes in the minor.
  `swim --version` takes its date from the tag when swim was installed with
  `go install …@<tag>` or `@latest`.

### Internal

- **e2e scenario suite** (`e2e/run.sh`, `make e2e`): `dag99`, a 99-lane DAG
  with every edge and data-flow value audited; `dag99-cascade`, where skips
  must equal exactly the descendants of the injected failures; and
  `dag99-retry`. It runs offline against fixtures, with 0–10ms of simulated
  work per lane.
  - The scenarios also run as subtests of `go test ./internal/e2e`, so
    `make check` and CI include them.
  - `e2etool`, written in Go, replaces the scenarios' Python helpers: only bash
    and Go are needed.
- **Make targets:** `make test` runs the unit tests, `make e2e` runs the Go e2e
  suite plus the scenarios (`S="dag99 …"` picks scenarios), and `make check`
  runs gofmt, vet, unit and e2e tests.

## 1.20261009 (pre-release, 67c0b33, 2026-10-09)

### Added

- **Versioning:** `swim --version` prints `<breaking>.<YYYYMMDD>`.
- **`.swim.lock`** pins the breaking version a repo's lanes use. It's written on
  the first run; commit it. When the lock and the binary disagree, swim refuses
  to run lanes or write new ones, and says whether to update swim or upgrade
  the repo. `swim plan`, `status` and `log` still work, with a warning.
- **`swim lock [--upgrade]`** shows this swim's version and the lock, and
  moves the lock up.

### Changed

- **The module path is now `github.com/gates-brightly/swimlane`.** Install it
  with `go install github.com/gates-brightly/swimlane/cmd/swim@latest`.

## dd2db6f (pre-release, 2026-10-09)

### Added

- **`# After: <lane|job> [...]`** in a script's header makes a lane wait for
  those lanes (or that exact job) to pass. Config `deps:` still works for
  ordering that holds every round.
- **`swim all [--rerun]`** runs every pending round that hasn't passed yet. A
  round that passed counts as done until its lane gets a new round.
- **`swim plan [N|JOB ...]`** shows the dependency tree, predicted skips and
  guard flags, and runs nothing.
- **`swim log [N|JOB] [--all]`** prints a lane's log (`--all` adds its
  archives), a job's rounds, or the project log.
- **`.swim.log`, the project log:** one line per top-level action (rounds
  written, runs, passes and fails, skips, archives, stubs, notes).
  `swim note [--lane N|JOB] "<text>"` adds to it.
- **`swim N [N ...]`** is shorthand for `swim run N ...`.
- **`swim config --lanes N`** sets the lane count. When lane scripts exist
  beyond it, `swim run` / `swim all` offer to raise it.

### Changed

- **Lane logs moved to `.swim/logs/agentN.log`,** and archives sit next to them.
  Logs at the repo root are moved automatically.
- **The `.gitignore` block** now covers `lane.[0-9]*.sh` and `.swim.log`.

## 64a0e07 (pre-release, 2026-10-09)

First version of swim: operator-in-the-loop automation. An AI planner and its
workers write bash lane scripts; a human operator runs them; every step lands in
a log the session reads back.

### Added

- **Commands:** `swim init`, `new N "<goal>"`, `run [N|JOB ...]`,
  `status [N|JOB] [--yaml]`, `step`, `lib`, `archive`, `stub`, `config` and
  `help`.
- **Lane library:** `lane_init`, `run`, `gate`, `snapshot`, `guard`,
  `confirm`, `last_failed`, `any_failed`, `drift`, `stop` and `summary`.
- **Running lanes:** lanes run in parallel and in dependency order (config
  `deps:`). The live view pins one row per lane at the top, and plain mode
  is used when output isn't a terminal. A run can be pinned to job ids.
- **Records:** each lane's log (`agentN.log`), `.swim/status.yml`, and
  snapshots in `.swim/snapshots/`.
