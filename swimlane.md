# Swimlane: product brief

A small utility for **operator-in-the-loop automation**. An AI session (the *planner*) and its subagents (the *workers*) write the operational work as runnable scripts. A human operator runs them in environments the session can't reach, such as a VPN-only account or their own credentials. Every action and result lands in a log the session reads back to decide the next step.

The pattern grew out of the SST → Terraform migration on `feature/tf-migration`, where it ran four parallel lanes across ~25 services.

---

## Problem

- AI sessions can write infrastructure changes but often can't (or shouldn't) run them. They lack the credentials, the network or the authority.
- Copying commands from chat into a terminal is error-prone: quoting breaks, steps get skipped, and output is pasted back partially.
- Parallel work collides. Two sessions overwrite each other's scripts, logs interleave, and nobody knows what is running.
- Destructive steps (removing a stack, deleting a table) need deliberate, recorded human approval, not an accidental Enter.
- Excessive token consumption when streaming logs to an agent

## Approach

- **The session writes, the operator runs, the log reports.** The planner never executes against the target. It writes a *round* (one runbook), the operator runs it, and the planner reads the log.
- **Lanes isolate parallel work.** Each lane is one runbook (`nextN.sh`) and one log (`agentN.log`), owned by one worker at a time.
- **One fixed launcher.** `next.sh` runs lanes, orders dependent lanes and summarises, and is never edited per round.
- **Rounds are disposable; logs are kept.** A finished round becomes a "nothing pending" stub, and its log is archived with a descriptive name.
- **Safe by default.** Destructive steps sit behind explicit flags, after a snapshot and a gate, and dry run is the default.

## Concepts

- **Lane:** a numbered slot (1..N) with one runbook and one log, worked on by one agent.
- **Round:** one runbook's contents, a single goal such as "cut over service X" or "diagnose Y". Rewritten per round.
- **Step:** one command run through the step wrapper, recorded with its header, output and exit code.
- **Gate:** a step whose failure stops the round before any later destructive step.
- **Guard flag:** an environment variable the operator sets to approve one destructive action, for example `FIN_ALLOW_DELETE_ZG_ITEMS=1`.
- **Dependency:** "lane B starts after lane A succeeds", declared once in the launcher.
- **Stub:** the placeholder a lane holds between rounds.
- **Archive:** a finished log renamed `agentN.prev-<what>.log`.

---

## Functional requirements

### Step wrapper (`step.sh`)
- Run any command, streaming its output to the terminal unchanged.
- Append the command, quoted so it can be re-run, plus its output to the lane's log.
- Record a header per step: UTC time, cwd, git branch@sha, runtime version, cloud profile, and key env (for example `STAGE`).
- Strip ANSI colour codes from the log only.
- Record the exit code and duration, and preserve the command's exit code.
- Flush captured output even if interrupted (Ctrl-C).
- Choose the log through an env var (`STEP_LOG`); `--new` starts a fresh log.

### Runbook (`nextN.sh`)
- Set its own log before the first step.
- Provide `run "<label>" <cmd>`, which records `PASS`/`FAIL  <label> (exit N)`.
- Provide `last_failed`, for stopping before destructive steps.
- Print a summary at the end, also appended to the log.
- Load the pinned toolchain itself; non-interactive shells don't load version managers.
- Keep going after a failed check by default; only gates stop the round.
- Start every multi-step change with a read-only snapshot of current state, saved for comparison.
- Keep destructive steps behind guard flags, with a dry-run message explaining how to approve.
- Make rounds rerunnable: gates must tell "ours" (tagged) from "theirs", so a rerun doesn't trip on its own resources.
- End with a verification step (for example "re-plan is a no-op") and any shared audit.

### Launcher (`next.sh`)
- `./next.sh`: run every lane that has a runbook, in parallel.
- `./next.sh 1 3`: run only the given lanes.
- `./next.sh --list`: show each lane's pending round (first `Round:` line), last result (pass/fail counts, time) and failed steps.
- Prefix console output `[N]` so parallel lanes stay readable.
- Resolve dependency chains (for example 4 → 2 → 1). Start a lane when its dependency succeeds, and **skip** it if the dependency fails.
- Declare dependencies in one small table that changes only when ordering changes.
- End with a summary per lane: exit code, pass/fail counts, failed steps.
- Never need editing per round.
- Run on the oldest shell in use (macOS bash 3.2): no associative arrays, no `mapfile`, no negative indexes.

### Planner / worker protocol
- The planner assigns each worker a lane number; a worker writes **only** its own `nextN.sh`.
- Workers never edit shared files (registries, plan docs, the launcher). They report exact lines, and the planner applies them.
- Workers prefer new modules over changing shared ones while other lanes run; any change to a shared module must leave existing users unchanged.
- **Never edit a runbook while it may be running.** Bash reads scripts incrementally, so a mid-run edit corrupts the run.
- Before writing a round, check the launcher's list and confirm with the operator which lanes are idle.
- After a run, the planner reads the log, validates real outcomes (not just PASS lines), records results, archives the log and stubs the lane.
- Out-of-scope findings go to a follow-up list, not into the round.
- Unique names across sessions: runbook and log names must not be reused by another session.

### Safety
- Snapshot before any change; back up stateful data (for example table items) before removal, and restore it after.
- Read the *deployed* state (for example a CloudFormation template's `DeletionPolicy`); don't assume it from code.
- Destructive or irreversible actions need an explicit guard flag per action, recorded in the runbook with the date and reason.
- Prefer reversible operations (disable or unsubscribe, keeping a saved copy) to deletes when ownership is unclear.
- Probes must not cause side effects: no messages, emails, payments or mutations. Use inputs rejected by validation, health routes and dry runs.
- Gates are fail-closed: an empty or unknown answer stops the round.
- Logs never contain secret values; print keys or lengths only.

### Observability
- One log per lane per round, append-only, readable without the terminal.
- Machine-friendly summary lines (`PASS`/`FAIL`/`SKIP`/`DRIFT <label>`) for the planner and the launcher.
- Archived logs keep history across rounds for later debugging.

---

## Non-functional

- Plain bash plus the target's own CLIs; no daemon, no server, no dependencies beyond what the repo already pins.
- Works over flaky VPN: steps are idempotent or detect their own prior success.
- Files are git-ignored (`next*.sh`, `agent*.log`, `.lane*.rc`); nothing operational lands in commits by accident.
- Readable by a human without the planner: each runbook's header states the round's goal and steps.

## Out of scope (v1)

- Running against targets from the planner's side; the operator's machine stays the only executor.
- Scheduling, retries across rounds, or a UI.
- Secret management; the runbook relies on the operator's existing credentials.

## Open questions

- Package as a tiny CLI (`swimlane init|run|list|archive|stub`), or keep it as three copyable files plus conventions?
- Configurable number of lanes, and named lanes (`lane org`) as well as numbers?
- A machine-readable summary (JSON) beside the log, so planners don't parse text?
- Lane claims or locks across sessions, for example a lock file holding the owning session, to prevent overwrites?
- Built-in archive and stub commands, so planners don't hand-roll `mv` and stub scripts?
- A standard guard-flag registry, so approvals are discoverable (`--list` shows which flags a round honours)?

## MVP

1. Generic `step.sh`, the launcher with dependencies and `--list`, and a `next.sh` template with `run`, `last_failed`, `summary` and the toolchain preamble.
2. An `init` that installs those files plus gitignore entries.
3. `archive N <what>` and `stub N <message>` helpers.
4. A one-page conventions doc for planners (the protocol and safety sections above).
