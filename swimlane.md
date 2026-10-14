# Swimlane: product brief

swim is a single Go binary for **operator-in-the-loop automation**. An AI session (the *planner*) and its subagents (the *workers*) write operational work as small bash scripts, one per numbered *lane*. A person runs them with `swim`, in places the AI can't reach: a VPN-only account, their own credentials, production. Every step, result and approval lands in plain-text logs the AI reads back to decide what to do next, and in a signed record that proves what ran and who approved it.

> The session writes. The operator runs. The log reports.

The pattern grew out of the SST → Terraform migration on `feature/tf-migration`, where it ran four parallel lanes across ~25 services as three copyable shell files (`next.sh`, `step.sh`, `nextN.sh`). swim is that pattern packaged as a CLI. Its own CI runs through it.

How-to and reference live in the [README](README.md) and `swim --help`. Feature specs live in [`features/`](features/README.md). This brief covers why swim exists and what it must guarantee.

---

## Problem

- **AI can write changes but shouldn't run them.** Sessions can write infrastructure changes, but often lack the credentials, the network or the authority to run them.
- **Copying from chat breaks.** Pasting commands into a terminal is error-prone: quoting breaks, steps get skipped, and output is pasted back partially.
- **Parallel work collides.** Two sessions overwrite each other's scripts, logs interleave, and nobody knows what is running.
- **Approvals aren't deliberate.** Destructive steps (removing a stack, deleting a table) need a recorded human approval, not an accidental Enter.
- **There's no proof.** After an AI-assisted change, nobody can show which script actually ran, what it did, or who approved it.
- **Logs cost tokens.** Streaming raw terminal output to an agent is slow and expensive.

## Positioning

swim is **a safety and audit layer for letting AI write production operations work without letting it run that work.**

| Alternative | What swim does differently |
|---|---|
| Agents that execute directly | The agent never touches the target. The operator's machine (or a worker they control) is the only executor. |
| Runbook and ops platforms | Rounds are disposable, AI-written scripts, not curated playbooks. No server is required. |
| Ticket or PR approvals | An approval is bound to the exact script (by SHA-256), signed with an SSH key, and recorded in a hash-linked chain anyone can verify. |

**Who it's for:**
- **Individual engineers** who have an AI planning infrastructure work and want to stop pasting commands from chat.
- **Teams** that need AI-assisted production changes to pass review and audit: two-person approval rules, signed approvals and a provable record.

Lead with the second when it applies. The chain of custody is the hardest part of swim for others to copy.

## Approach

- **The session writes, the operator runs, the log reports.** The planner never runs anything against the target. It writes a *round*, the operator runs it, and the planner reads the log.
- **Lanes isolate parallel work.** Each lane is one script (`lane.N.sh`) and one log, owned by one worker at a time.
- **One fixed runner.** `swim` runs lanes, orders dependent ones and summarises. Nothing is edited per round except the lane script.
- **Rounds are disposable; logs are kept.** A finished lane is archived and stubbed, and its log is kept under a descriptive name.
- **Safe by default.** Destructive steps sit behind guards, after a snapshot and a gate. Without an approval they are dry runs.
- **Approvals bind to exact scripts.** Approving a script approves its hash. Editing the script voids the approval.
- **Everything is provable.** Every round and approval is signed and hash-linked, so tampering is detectable.
- **One workflow everywhere.** The same rounds run from a terminal, a queue, CI or many machines, without being rewritten.

## Concepts

- **Lane:** a numbered slot (`swim 1..N`) with one script and one log, worked on by one worker at a time.
- **Round:** what a lane script holds: one goal, such as "cut over service X" or "diagnose Y". Rewritten per round.
- **Job:** a round's unique id. Naming a job pins a run to that exact round.
- **Step:** one command run through `run`, `gate` or `snapshot`, recorded with its command, output and exit code.
- **Stage:** every round's fixed phases, in order: `snapshot`, `check`, `change`, `verify`.
- **Gate:** a step whose failure stops the round before any later destructive step.
- **Guard:** a named approval in front of a destructive step, for example `FIN_ALLOW_DELETE`.
- **Dependency:** `# After: 2 3` starts a lane once those lanes pass, and skips it if one fails.
- **Lock:** `# Locks: name` keeps lanes that share a resource from overlapping, without ordering them.
- **Always lane:** `# Always: yes` makes a lane standing setup, like a CI `before_script`: lint, fmt, init. It never counts as passed, so every run that includes it runs it fresh, first, and every other lane in that run waits for it to finish. Only lanes that name it in `After:` need it to pass, and naming it pulls it into a run. With `# Rerun-Children: yes`, `swim all` also reruns every lane downstream of it, at any depth, even if they passed. An Always lane waits only on other Always lanes, and isn't stubbed or archived without `--force`.
- **Batch:** lanes queued together, pinned to their exact scripts, run later by a worker.
- **Chain of custody:** the signed, hash-linked record of every round and approval.
- **Stub / archive:** the placeholder a lane holds between rounds / a finished log kept under a new name.

---

## Requirements

### Steps
- Run any command, streaming its output to the terminal unchanged.
- Log the command (quoted so it can be re-run), its output, exit code and duration. Keep the command's exit code.
- Record a header: UTC time, cwd, git branch@sha, and configured env (for example `STAGE`, `AWS_PROFILE`).
- Strip ANSI colour from the log only. Flush captured output even when interrupted.
- Support per-step timeouts and retries, for idempotent steps only.

### Rounds
- Declare the goal and metadata in a header. Only the goal is required.
- Keep going after a failed check by default; only gates stop the round.
- Start every multi-step change with a read-only snapshot of current state.
- Keep destructive steps behind guards, with a dry-run message that explains how to approve.
- Make rounds rerunnable: gates must tell "ours" (tagged) from "theirs", so a rerun doesn't trip on its own resources.
- End with verification (for example "re-plan is a no-op") and a summary.
- Run on the oldest shell in use (macOS bash 3.2).
- Be checkable before running (`swim lint`): header, stages, guards, bash 3.2, blocked commands, echoed secrets.

### Running
- Run every pending lane, or chosen lanes or jobs, in parallel up to a limit.
- Resolve dependencies; refuse a cycle before anything starts.
- Show a live view of every lane, with output prefixed `[N]`, and work without a terminal (plain lines or YAML).
- Show what would run before running it (`swim plan`), and who waited on whom afterwards (`swim timeline`).
- Stop gracefully on the first Ctrl-C, interrupt on the second, kill on the third.

### Approvals
- A guard approves in this order: its flag, signed approvals, the person at the terminal, `-y`. Anything else is a dry run.
- Signed approvals use the approver's SSH key and cover one exact script or, as a standing approval, every job one person queues.
- `lookup.yml` maps people to keys and groups, and sets how many approvals, from which group, each guard needs.
- Approvals are committed files, so approvers can add theirs from anywhere.

### Chain of custody
- Record every round (script hash, log-section hash, result, approvals used) and every approval (who, which guard, how).
- Sign each entry and link it to the one before. Keep every script that ran, by hash.
- `swim verify` proves nothing was edited, removed or reordered. Signatures are checkable with plain `ssh-keygen`, without swim.

### Queue, CI and hub
- **Queue:** a batch is fixed when queued. Its approvals travel with it, and the worker's own environment approves nothing. Several workers can share one queue safely.
- **CI:** `swim ci` never prompts. It folds each lane's log, turns failures into annotations, can run only the rounds a push changed, and never commits results back.
- **Hub:** a read-only dashboard of many workers. Workers push to it, so they can sit behind NAT.

### Planner / worker protocol
- The planner assigns each worker a lane number. A worker writes **only** its own `lane.N.sh`.
- Workers never edit shared files (registries, plan docs, config). They report exact lines, and the planner applies them.
- Workers prefer new modules over changing shared ones while other lanes run. Any change to a shared module must leave existing users unchanged.
- **Never edit a lane while it may be running.** Bash reads scripts incrementally, so a mid-run edit corrupts the run. swim refuses to edit, archive, stub or rerun a running lane.
- Before writing a round, check `swim status` and confirm with the operator which lanes are idle.
- After a run, the planner reads the log, validates real outcomes (not just PASS lines), records results in the project log, archives the log and stubs the lane.
- Always lanes (`# Always: yes`) are the repo's standing setup, not rounds: planners never assign, rewrite, archive or stub them. A round that needs one passing says so with `# After:`.
- Out-of-scope findings go to a follow-up list, not into the round.
- The full protocol for agents is `swim --help`.

### Safety
- swim never pushes, commits or pulls. It blocks those commands in scripts, at runtime and through a git shim, and a repo can block more.
- Snapshot before any change. Back up stateful data (for example table items) before removing it, and restore it after.
- Read the *deployed* state (for example a CloudFormation template's `DeletionPolicy`); don't assume it from code.
- Prefer reversible operations (disable or unsubscribe, keeping a saved copy) to deletes when ownership is unclear.
- Probes must not cause side effects: no messages, emails, payments or mutations. Use inputs rejected by validation, health routes and dry runs.
- Gates and confirmations fail closed: no answer stops the round.
- Mask secrets everywhere swim writes. Masking is a backstop; rounds still never print secrets.

### Observability
- One plain-text log per lane per round, readable with `cat` and greppable by line prefix.
- A project log (`.swim.log`): one line per top-level action, plus decisions, for a new agent to catch up from.
- A versioned YAML form of every read command, and exit codes that waits can rely on, so agents don't parse text or stream output.
- Archived logs keep history across rounds.

---

## Non-functional

- **One binary.** swim is one Go binary plus the target's own CLIs. The queue worker and hub are optional; nothing needs a daemon to run a round.
- **Flaky networks.** Rounds work over a flaky VPN: steps are idempotent or detect their own prior success.
- **Nothing operational is committed by accident.** Lane scripts, logs and state are git-ignored by default. Shared settings, `lookup.yml`, approvals and `.swim.lock` are committed.
- **Readable without the planner.** A round's header states its goal, and its log shows every step.
- **Personal settings stay out of reach.** Config lives outside the repo, so workers can't change it.
- **Upgrades are explicit.** `.swim.lock` pins the breaking version a repo uses, and older scripts and logs are migrated with the originals kept.

## Out of scope

- **Running from the planner's side.** The operator's machine, or a worker they control, stays the only executor.
- **Secret management.** Rounds use the operator's existing credentials.
- **Writing to git.** swim never commits, pushes or pulls, including in CI.
- **Running or approving from the hub.** The hub only shows state.
- **Scheduling across rounds.** The queue runs batches when swim is idle; there is no cron.

## Open questions

- **Named lanes:** support `lane org` alongside numbers?
- **Cross-session claims:** should a worker claim a lane before writing it, beyond the `Owner` header and running-lane protection?
- **Keeping the chain:** `.swim/chain.jsonl` isn't committed. Should swim export or ship it somewhere durable by default?
- **Hub access:** is a shared token enough, or does the hub need per-user identity from `lookup.yml`?
- **Beyond bash:** should rounds ever be written in something other than bash, or is bash's universality (and models' fluency in it) the point?

### Resolved from v1

- **CLI or copyable files?** A CLI: one Go binary.
- **Configurable lane count?** Yes: config `lanes`.
- **Machine-readable summary?** Yes: versioned YAML for every read command.
- **Built-in archive and stub?** Yes: `swim archive` and `swim stub`.
- **Guard registry?** The `Guards` header, guards found from `guard` calls, `swim lint` and `lookup.yml` rules.
- **Scheduling, retries, a UI?** Shipped beyond the v1 scope: the queue, per-step retries, the live view and the hub.
