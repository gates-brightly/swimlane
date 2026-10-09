# swim

**Operator-in-the-loop automation.** An AI session (the *planner*) and its
subagents (the *workers*) write operational work as bash scripts, one per
*lane*. A human operator runs them with `swim`, in places the session can't
reach (a VPN-only account, their own credentials). Every step and result
lands in a log the session reads back to decide what to do next.

> The session writes. The operator runs. The log reports.

```
swim · myrepo · main@64a0e07 · 0:04
 swim 1  PASS 0:01                  Diamond root: fetch pages              51c7cf2e
 swim 2  PASS 0:03                  Child A: convert to Markdown           d7af1473
 swim 3  / running 0:04             Child B: link stats                    0acffc31
 swim 4  / waiting on swim 3        Join: build report                     db8e71f9
──────────────────────────────────────────────────────────────────────────────────
[3] ==> compute stats.json
[3] $ e2etool stats
[3] cern     words=  173 int= 25 ext=  0  The World Wide Web project
[3] PASS  compute stats.json  0.1s
```

The product brief is in [`swimlane.md`](swimlane.md).

---

## Install

swim is a single Go binary. Building it needs Go **1.27 or newer**.

### With `go install` (recommended)

```sh
go install github.com/gates-brightly/swimlane/cmd/swim@latest
```

This builds `swim` into `$GOBIN`, or into `$(go env GOPATH)/bin` (usually
`~/go/bin`) if `GOBIN` is unset. Make sure that directory is on your `PATH`:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"   # add to ~/.zshrc or ~/.bashrc
swim --version                             # e.g. swim 2.20261009
```

- **Pin a version:** replace `@latest` with a release tag (`@v0.2.20261009`)
  or a commit (`@64a0e07`).
- **Update:** run the same `go install` command again.
- **Uninstall:** `rm "$(go env GOPATH)/bin/swim"`.

### From a checkout

```sh
git clone git@github.com:gates-brightly/swimlane.git && cd swimlane
make install     # go install, with the build date and commit stamped in
# or
make link        # build bin/swim and symlink it into ~/.local/bin (LINK_DIR=... to change)
```

### Managing installs

You can have both a `go install` copy and a `make link` copy; whichever comes
first on your `PATH` runs.

```sh
make which            # every swim on your PATH, which one runs, where each came from
make install-latest   # go install the published swim (V=v0.2.20261009 to pin)
make uninstall        # remove the go-installed swim
make link / unlink    # add / remove the symlink to this checkout's bin/swim
```

`make help` lists every target.

### Versions

`swim --version` prints `<breaking>.<YYYYMMDD>`, for example
`swim 2.20261009 (64a0e07)`:

- the **breaking** number changes only when lane scripts, logs or swim's
  state change incompatibly;
- the **date** is the build date.

The first `swim run` / `swim all` in a repo writes **`.swim.lock`**, which
records the breaking version the repo uses. Commit it. If someone runs a swim
with a different breaking version, swim refuses to run lanes and says how to
fix it:

- **Your swim is older than the lock:** update swim (`go install …@latest`,
  or `git pull && make install`) and run again.
- **Your swim is newer than the lock:** the repo predates a breaking change.
  Update the lane scripts, then run `swim lock --upgrade`. `swim lock --help`
  lists what each breaking version changed.

`swim plan`, `status` and `log` still work under a mismatch, with a warning.

---

## Quick start

```sh
cd your-repo
swim init                                  # config entry, .gitignore block, .swim/status.yml
swim new 1 "Cut over orders-api"           # writes lane.1.sh with a new job id
$EDITOR lane.1.sh                          # (or let an AI planner write the steps)
swim plan                                  # dry run: what swim all would do
swim all                                   # run every pending lane, live view
swim status                                # last state of every lane
swim log 1                                 # lane 1's log
swim archive 1 && swim stub 1 "orders done"   # keep the log, free the lane
```

**AI agents** should start with `swim --help`. It is the full guide for
planners and workers: the protocol, the lane script library, the safety
rules, and how to read results.

---

## Concepts

| Term | Meaning |
|---|---|
| **lane** | A numbered slot, `swim 1..N`. One script (`lane.N.sh`) and one log, owned by one worker at a time. |
| **round** | One lane script's contents: a single goal ("cut over service X"). Disposable. |
| **job** | A round's unique id (the `# Job:` line). It is recorded everywhere, exported to steps as `$SWIM_JOB`, and usable in place of a lane number. |
| **step** | One command run through `run` / `gate` / `snapshot`, logged with a header, its output and its exit code. |
| **gate** | A step whose failure stops the round before any later (destructive) step. |
| **guard flag** | An env var the operator sets to approve one destructive action, e.g. `FIN_ALLOW_DELETE=1`. Unset means dry run. |
| **dependency** | `# After: 1` in a lane script: the lane waits for swim 1 to pass, and is skipped if it doesn't. |
| **lock** | `# Locks: orders-table` in a lane script: lanes sharing a lock never run at the same time (in either order, within a run and across concurrent runs), and a failure doesn't spread through a lock. |
| **stub / archive** | Between rounds a lane holds a "nothing pending" stub, and its finished log is archived. |

---

## A lane script

`swim new` writes a template. A filled-in round looks like this:

```bash
#!/usr/bin/env bash
# swim: syntax 2
# Round:   Cut over orders-api from SST to Terraform
# Job:     3f2a9c1e-7b4d-4e2a-9c1e-7b4d4e2a9c1e
# After:   1
# Owner:   zach
# Created: 2026-10-09
# Guards:  ORDERS_ALLOW_SST_REMOVE  remove SST stack (2026-10-09: tf owns it)
# Locks:   tf/orders
# Timeout: 30m
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2

stage snapshot
snapshot "cfn template" aws cloudformation get-template --stack-name orders-prod
stage check
run  "tf plan" terraform -chdir=infra/orders plan -detailed-exitcode
gate "DeletionPolicy is Retain (deployed)" \
  bash -c 'aws cloudformation get-template --stack-name orders-prod | grep -q Retain'
stage change
if guard ORDERS_ALLOW_SST_REMOVE "remove SST stack orders-prod (2026-10-09: tf owns it)"; then
  run "sst remove" npx sst remove --stage prod
fi
stage verify
run "verify: re-plan is a no-op" terraform -chdir=infra/orders plan -detailed-exitcode
summary
```

- **Header:** `Round` is required. Everything else has a default when
  missing or empty:
  - `Job`: generated when the round starts
  - `After`: none
  - `Owner`: `-`
  - `Created`: the file's date
  - `Guards`: found from `guard` calls in the body
  - `Locks`: none
  - `Timeout`: none

  Unknown keys are kept and shown in the log. A `Timeout` stops the round
  when it runs out.
- **Stages:** every job has the same four, in order: `snapshot` (read-only
  capture), `check` (checks and gates), `change` (guarded changes) and
  `verify` (prove it worked). A job skips the ones it doesn't need. Steps
  before the first `stage` are "setup". Out-of-order stages, or `change`
  without a snapshot and check before it, log a `WARN`.
- **Library functions** (from `swim lib`): `lane_init`, `stage`, `run`, `gate`,
  `snapshot`, `guard`, `confirm`, `last_failed`, `any_failed`, `drift`,
  `stop` and `summary`.
- **Shell:** the library runs on macOS's bash 3.2.
- **Failures:** a failed check doesn't stop the round; only gates do. Don't
  use `set -e`.

### The lane log

Logs are plain text, built to be read with `cat` or an editor. Every line
says what it is by how it starts, and command output is always fenced with
`| `, so `grep '^  FAIL' .swim/logs/agent2.log` finds every failure:

```
== ROUND 2026-10-09T12:00:00Z  job=3f2a9c1e-...  Cut over orders-api
   script: lane.2.sh | owner: zach | after: 1 | timeout: 30m | git: main@abc1234

-- stage check  12:00:02
  FAIL  tf plan (exit 2)                        3.1s  12:00:02
        $ terraform -chdir=infra/orders plan -detailed-exitcode
        | Error: ...
  STOP  gate failed: tf plan

== END FAIL  pass=1 fail=1 skip=0 drift=0  exit=1  3.6s  2026-10-09T12:00:04Z
   stages: snapshot PASS | check FAIL | change none | verify none
   failed: FAIL  tf plan (exit 2); STOP  gate failed: tf plan
```

On a terminal, `swim log 2` renders it: colours, times relative to the
round start, and long output folded (`--full` to expand). Piped or with
`--raw`, it is the file as is.

### Syntax versions

Lane scripts (`# swim: syntax 2`) and logs (`# swim lane log | syntax 2`)
declare their syntax. swim migrates older files automatically the next time
it loads the repo: they are rewritten in place, the originals are copied to
`.swim/migrations/<time>/`, and the migration is recorded in `.swim.log`. A
running lane's files are left until it finishes. A file in a newer syntax
stops swim with instructions to update it.

---

## Commands

| Command | What it does |
|---|---|
| `swim init` | Set up the repo: config entry, `.gitignore` block, `.swim/status.yml`. |
| `swim new N "<goal>" [--job ID]` | Write `lane.N.sh` from the template with a new job id. |
| `swim plan [N\|JOB ...] [--rerun]` | Dry run in Terraform style (`[+]` run, `[~]` retry, `[+/-]` rerun, `[-]` skip), shown as a dependency tree with guard flags. |
| `swim all [--rerun] [--parallel N]` | Run every pending job that hasn't passed yet, at most N lanes at once if set. Offers to add lanes if scripts exist beyond the configured count. |
| `swim run N\|JOB ...` / `swim N ...` | Run specific lanes, even if they already passed. A job id pins the run to exactly that job. |
| `swim status [N\|JOB] [--yaml]` | Last state of every lane, from `.swim/status.yml`. |
| `swim log [N\|JOB] [--all] [--full] [--raw]` | A lane's log (`--all` adds its archives), a job's rounds, or with no argument the project log; rendered on a terminal. |
| `swim archive N\|JOB [<what>]` | Archive a lane's log (the name defaults to the job id). |
| `swim stub N\|JOB "<message>"` | Replace a lane script with a "nothing pending" stub. |
| `swim note [--lane N] "<text>"` | Record a decision or finding in the project log. |
| `swim config [--path] [--lanes N]` | Show the effective config, or set the lane count. |
| `swim lock [--upgrade]` | Show swim's version and the repo's `.swim.lock`, or move the lock up a breaking version. |
| `swim step -- cmd ...` | Run and log one command (what `run` calls). |
| `swim lib` | Print the bash library lane scripts load. |
| `swim --version` | Print the version: `<breaking>.<YYYYMMDD>`. |
| `swim --help` / `swim <cmd> --help` | Print the full guide, or one command's detail. |

### The live view

While lanes run, a panel pinned to the top of the terminal shows each lane's
state and round, with a throbber and `waiting on swim N` for lanes blocked on
a dependency. Output scrolls underneath, prefixed `[N]`.

- **More than 15 lanes:** idle and finished lanes fold into a `… N more`
  line, so running lanes stay visible.
- **Ctrl-C:** reaches the running lanes, skips the rest, and still prints
  the summary.
- **No terminal (CI, pipes) or `--plain`:** output is plain prefixed lines.

---

## Dependencies

A round declares what it waits for in its header:

```bash
# After: 2 3          # lane numbers, or job ids for "that exact round"
```

`swim plan` shows the result as a tree:

```
[+] swim 1  Diamond root: fetch pages
├── [+] swim 2  Child A: convert to Markdown
│   └── [+] swim 4  Join: build report  wait [2,3]
└── [~] swim 3  Child B: link stats
    └── [+] swim 4 (shown above)

Plan: 3 to run, 1 to retry, 0 to rerun, 0 to skip.
```

Ordering that holds for every round can also go in config (below). A cycle,
or a reference that doesn't resolve, stops the run before anything starts.

---

## Files

In the repo:

| Path | Contents |
|---|---|
| `lane.N.sh` | Lane N's script (the current round). |
| `.swim.lock` | The breaking version of swim this repo uses. **Commit it.** |
| `.swim.log` | Project log: one line per top-level action (rounds written, run, passed, failed, archived, notes). A new agent reads this first. |
| `.lane.N.rc` | Optional operator-local env for lane N. |
| `.swim/status.yml` | Last state of every lane. `cat` it, or use `swim status`. |
| `.swim/logs/agentN.log` | Lane N's log: append-only, plain text. Read it with `swim log N`. |
| `.swim/logs/agentN.prev-*.log` | Archived logs. Read them with `swim log N --all`. |
| `.swim/snapshots/` | Output saved by `snapshot` steps. |
| `.swim/migrations/` | Originals of files migrated to a newer syntax. |

`swim init` git-ignores everything above except `.swim.lock`.

Configuration lives outside the repo, so workers can't change it:

```yaml
# ~/.config/swim/config.yml  ($XDG_CONFIG_HOME/swim/config.yml)
defaults:
  lanes: 4
  header_env: [STAGE, AWS_PROFILE]       # env recorded in every step header (never secrets)
  runtime: node --version                # recorded as the runtime version
  toolchain: . "$HOME/.nvm/nvm.sh" && nvm use >/dev/null
repos:
  /path/to/your-repo:                    # git toplevel; overrides defaults
    lanes: 6
    max_parallel: 8                      # lanes running at once (default unlimited)
    deps: {2: [1], 4: [2, 3]}            # ordering that holds every round
```

---

## Safety

- **Guarded destructive steps:** each one sits behind a gate and has its own
  guard flag. With the flag unset, the step dry-runs and the log shows how to
  approve it.
- **Snapshot first:** every multi-step change starts with a read-only
  snapshot.
- **Fail-closed:** gates and `confirm` prompts stop the round when there is
  no answer.
- **Clean logs:** ANSI codes are stripped, and only the env keys you list
  are recorded, never secrets.
- **Running lanes are protected:** swim refuses to edit, archive, stub or
  rerun a lane that is running, and refuses to run under a breaking-version
  mismatch.

The full rules for planners and workers are in `swim --help`.

---

## Development

```sh
make test             # unit tests (fast)
make e2e              # end-to-end: the Go e2e suite and every scenario
make e2e S=dag99-retry   # just the named scenarios
make check            # gofmt, go vet, unit and e2e tests (what CI runs)
make build            # bin/swim
go test ./internal/display -update   # regenerate the panel golden files
```

There are two layers of end-to-end tests. Both build the binary and drive it
against scratch repos:

- **`internal/e2e`** (Go) checks commands one at a time: init, new, run,
  plan, log, archive, interrupts, locks.
- **`e2e/scenarios`** (bash, with the Go helper `e2e/cmd/e2etool`) runs whole lane DAGs offline: 99 lanes with real
  work over fixture pages, injected failures, and a retry. They guard the
  scheduler, dependencies, `swim all` and `swim plan` against regressions.
  `make e2e` (and `go test ./internal/e2e`) runs them; `go test -short` skips them. See
  [`e2e/README.md`](e2e/README.md) for writing one.

CI (`.github/workflows/test.yml`) runs `make check` on Linux and on macOS,
where it uses the system `/bin/bash` 3.2 so the lane library and the scenario
runner are checked against it.

- **Bash 3.2:** `internal/assets/lib.sh`, `e2e/run.sh` and `e2e/lib/common.sh`
  must stay compatible with macOS bash 3.2.
- **Breaking changes:** bump `Breaking` in `internal/version/version.go`, and
  add a line to the "Breaking versions" list in `swim lock --help`
  (`internal/help/help.go`).
- **Releases** are git tags `v0.<breaking>.<YYYYMMDD>`, e.g. `v0.2.20261009`
  for swim `2.20261009`. Go requires a `/vN` module path for majors above 1,
  so swim's breaking number lives in the tag's minor. A release is a tag on a
  commit where `make check` passes:

  ```sh
  git tag -a v0.2.$(date -u +%Y%m%d) -m "swim 2.$(date -u +%Y%m%d)"
  git push origin main --follow-tags
  ```

  `go install …@latest` then picks the newest tag, and `swim --version` reads
  its date from the tag.

Layout: `cmd/swim` (entry point), `internal/cli` (commands),
`internal/launcher` (runs lanes, dependencies, plan), `internal/display`
(live view), `internal/step` (step wrapper), `internal/status`,
`internal/history` (project log), `internal/logparse`, `internal/lane`,
`internal/config`, `internal/version` (version and `.swim.lock`),
`internal/assets` (bash library and templates), `internal/help` (the guide).
