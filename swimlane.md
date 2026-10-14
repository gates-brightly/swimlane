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
- **Work keeps restarting from zero.** The state of the work lives in a chat. When the session ends, its context is summarised or another agent takes over, someone has to explain it all again.
- **Each side waits on the other.** The AI stalls until the operator runs a step and pastes back the result; the operator is interrupted for every step instead of approving a batch.

## Pillars

swim is a flywheel for AI-assisted operations work. Every round leaves behind state that is durable and readable: a log, a status file, an archive. The next round, session or person starts from that state instead of starting over. The control pillars are what make it safe to turn the flywheel fast.

1. **The AI writes but can't reach.** The planner never needs production credentials, network access or authority. The operator's machine, or a worker they control, is the only thing that executes.
2. **The log closes the loop.** Results go back to the AI as compact plain text it can parse, not as output pasted into chat. That keeps context small and needs no human to carry results.
3. **State lives in the repo, not the chat.** Lane scripts, dependencies, `.swim/status.yml`, `.swim.log` and archived logs outlive any session. A fresh session catches up with `swim log` and continues.
4. **Neither side blocks the other.** The AI queues the next batch while the operator is busy or away; the operator approves in batches, or ahead of time, and a worker runs the work.
5. **Concurrent work stays structured.** Lanes isolate parallel work, dependencies order it and locks keep shared resources from overlapping.
6. **Approvals are deliberate and exact.** Destructive steps are dry runs until a person approves them, and an approval is signed against the exact script.
7. **Everything is provable.** Every round and approval is signed and hash-linked into an audit record anyone can verify.
8. **One workflow everywhere.** The same rounds run from a terminal, a queue, CI or many machines, without being rewritten.

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

## What swim protects against

Approval, signing and the chain prove **who** decided **what**. They don't make an arbitrary script safe. Every feature should make that line sharper, not blur it.

- **Authorization:** nothing that needs approval runs without valid authorization for that exact operation. A guard approves by its flag, signed approvals, the person at the terminal or `-y`, and otherwise is a dry run; it fails closed when there's no one to ask, the approval socket fails, the round times out or the run stops. Under `swim run` the launcher decides and records every approval: a lane can't approve itself, set its own flag, or report for another lane. The guard itself is a bash function in the lane: advisory, not enforced.
- **Integrity:** what runs matches what was approved. Approvals bind to an execution manifest (the script, the files it sources or includes, its sidecar, its runner, and the policy part of swim.yml), and each lane runs a staged copy of exactly that, so an edit mid-wait or mid-run never reaches it and an approval stops counting once anything it pins changes. A queued lane whose manifest changed is skipped.
- **Attribution:** every run traces back to its job, worker, approval and outcome, in a hash-linked, SSH-signed chain that records how each guard was approved. `swim verify` reports how far the chain can be believed: consistent, trusted (signers in a `trust.yml` pinned outside the repo), anchored (checkpoints sent where a lane can't rewrite them) and authorized.
- **Out of scope:** a malicious or mistaken script that is approved and run with broad credentials. That needs scoped credentials and OS isolation; [features/guarded-secrets.md](features/guarded-secrets.md) designs releasing a credential only after its guard is approved.

swim is a tool a person operates, with teammates approving from their own machines, not a multi-user production executor. The full model, with the test behind each claim, is [docs/trust-model.md](docs/trust-model.md).

## Approach

- **The session writes, the operator runs, the log reports.** The planner never runs anything against the target. It writes a *round*, the operator runs it, and the planner reads the log.
- **Lanes isolate parallel work.** Each lane is one script (`lane.N.sh`) and one log, owned by one worker at a time.
- **One fixed runner.** `swim` runs lanes, orders dependent ones and summarises. Nothing is edited per round except the lane script.
- **Rounds are disposable; logs are kept.** A finished lane is archived and stubbed, and its log is kept under a descriptive name.
- **State is files, not memory.** Everything a planner needs to resume, including what ran, what is running, what is queued and what failed, is in the repo, readable with `cat`.
- **Hand-offs are asynchronous.** The queue, `swim watch` and standing approvals let the planner and operator each work at their own pace.
- **Safe by default.** Destructive steps sit behind guards, after a snapshot and a gate. Without an approval they are dry runs.
- **Approvals bind to exact scripts.** Approving a script approves its hash. Editing the script voids the approval.
- **Everything is provable.** Every round and approval is signed and hash-linked, so tampering is detectable.
- **One workflow everywhere.** The same rounds run from a terminal, a queue, CI or many machines, without being rewritten.

## Concepts

- **Lane:** a numbered slot (`swim 1..99`) with one script and one log, worked on by one worker at a time. A repo's lanes are the ones in use: a script that isn't a stub, a current log, or a running lane. `swim archive N` drops one.
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
- **Job:** part of a lane. Any bash lane can be split into jobs (`# swim job 1.1`), nested to any depth (`1.5.9.9`). Jobs run in file order and the first failure skips the rest. A job id names it and every job under it, in `swim run 1.5` and `After: 1.5`. Other lanes wait only for a lane's Always jobs.
- **Group file:** `lanes.<name>.sh`, a lane of jobs with a name instead of a number, for standing jobs worth committing.
- **Stub / archive:** the placeholder a lane holds between rounds / a finished log kept under a new name.

---

## Requirements

### Steps
- Run any command, streaming its output to the terminal unchanged.
- Log the command (quoted so it can be re-run), its output, exit code and duration. Keep the command's exit code.
- Record a header: UTC time, cwd, git branch@sha, and configured env (for example `STAGE`, `AWS_PROFILE`).
- Strip ANSI colour from the log only. Flush captured output even when interrupted.
- Support per-step timeouts and retries, for idempotent steps only. A timeout stops everything the step started (its process group: SIGTERM, then SIGKILL), except a step reading the terminal, which stays in the lane's group.

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
- Run the bytes that were approved: pin each lane's execution manifest (script, the local files it sources or imports, sidecar, runner, the policy in `swim.yml` and `lookup.yml`) before it waits, run a read-only staged copy, and check it again right before exec.

### Approvals
- A guard approves in this order: its flag, signed approvals, the person at the terminal, `-y`. Anything else is a dry run.
- Signed approvals use the approver's SSH key and cover one exact execution manifest or, as a standing approval, every job one person queues. A stale approval says what changed.
- The question comes from swim, not the lane: the launcher names the lane, job and manifest (each lane process has its own token), labels the lane's description as the lane's, refuses undeclared flags, and lets each approval be used once.
- Facts swim can't pin (the cloud account, binaries on `PATH`) are `Observe:` probes, read at approval and again before the guarded step; a change ends the round as drift.
- `lookup.yml` maps people to keys and groups, and sets how many approvals, from which group, each guard needs.
- Approvals are committed files, so approvers can add theirs from anywhere.

#### Approval lifecycle

A signed approval is in one of five states, shown by `swim approve` and `swim verify`:

- **Active:** it counts.
- **Expired:** past its expiry. `--expires D` sets one on any approval.
- **Revoked:** every signer's latest event revokes.
- **Stale (why):** what it approves changed. A script approval binds its execution manifest: the script, what it pins, `swim.yml`'s policy keys and all of `lookup.yml`'s `require:` rules, so any of them changing stales it. A standing approval binds the rule each guard had when it was signed: once that rule changes, stricter or looser, it stops counting for that guard. Loosening a rule never turns an approval signed under the stricter one into enough; the approvers sign again.
- **Insufficient (why):** its signers don't meet the guard's rule, aren't in `lookup.yml`, or (standing) it outlasts `standing.max`.

Policy changes and membership are treated differently on purpose. A rule is what approvers agreed to, so a change to it needs their agreement again. Who is who (users, keys, groups' members) is read live: a key removed from `lookup.yml` stops counting at once, and adding a person to a group needs no re-approval.

Standing approvals (`swim approve --auto USER`) must expire: 30 days by default (printed), never longer than `standing: {max: D}` in `lookup.yml`. Approving again once one stopped counting starts a new one. Standing approvals from before this (never expiring, no rules bound) keep counting, with a warning in `swim approve`, `swim doctor` and `swim verify`, until `lookup.yml` sets `standing.max`; then they stop.

### Chain of custody
- Record every round (script and manifest hashes, log-section hash, result, approvals used) and every approval (who, which guard, how).
- Sign each entry and link it to the one before. Keep every script that ran, by hash.
- `swim verify` proves nothing was edited, removed or reordered. Signatures are checkable with plain `ssh-keygen`, without swim.

### Queue, CI and hub
- **Queue:** a batch is fixed when queued. Its approvals travel with it, and the worker's own environment approves nothing. Several workers can share one queue safely.
- **CI:** `swim ci` never prompts. It folds each lane's log, turns failures into annotations, can run only the rounds a push changed, and never commits results back.
- **Hub:** a dashboard of many workers. Workers push to it, so they can sit behind NAT. It serves https with `--tls-cert`/`--tls-key`, and plain HTTP only on loopback unless `--insecure`. Hub users (each with their own token and a viewer or operator role) can send headless workers commands: interrupt, drain, run, fetch a log. The hub only relays them; the worker checks each against the repo's committed `headless:` settings and records it.

### Planner / worker protocol
- The planner assigns each worker a lane number. A worker writes **only** its own `lane.N.sh`.
- Workers never edit shared files (registries, plan docs, config). They report exact lines, and the planner applies them.
- Workers prefer new modules over changing shared ones while other lanes run. Any change to a shared module must leave existing users unchanged.
- **Never edit a lane while it may be running.** Under `swim run` a lane runs a staged copy, so an edit no longer reaches it, but a lane run directly with bash still reads its script incrementally. swim refuses to edit, archive, stub or rerun a running lane.
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
- **Upgrades are explicit.** `.swim.lock` pins the syntax versions a repo uses (lane scripts, logs, state), and older scripts and logs are migrated with the originals kept. Releases (`0.<date>.<index>`) that change no syntax never touch it.

## Out of scope

- **Running from the planner's side.** The operator's machine, or a worker they control, stays the only executor.
- **Secret management.** Rounds use the operator's existing credentials.
- **Writing to git.** swim never commits, pushes or pulls, including in CI.
- **Executing on the hub.** The hub relays requests to headless workers, which check, carry out and record them. It never runs anything itself, never holds credentials or secrets, and can't make an approval valid by itself: an answer to a guard counts when the worker verifies the approver's SSH signature against `lookup.yml` (or, where the repo's `swim.yml` allows it, a listed hub user's click for a guard no `require:` rule names).
- **Scheduling across rounds.** The queue runs batches when swim is idle; there is no cron.

## Open questions

- **Named lanes:** support `lane org` alongside numbers?
- **Beyond bash:** should rounds ever be written in something other than bash, or is bash's universality (and models' fluency in it) the point?

### Resolved since

- **Hub access?** Per-user identity: the hub's admin adds users (`swim hub user add NAME --role viewer|operator`), named as in `lookup.yml`, each with their own token and a browser session. Every command carries the user who sent it, and the worker records it. The shared tokens only enroll workers and manage the hub. Approving through the hub takes the approver's own SSH signature, checked by the worker: the hub login alone approves only where a repo opts in (`headless.approvals: hub`). See `swim watch --help` and `swim hub --help`.
- **Keeping the chain?** Opt-in, not by default: `chain_anchors` sends a signed checkpoint of the chain's head to a `file:` path outside the repo or the hub at the end of every run, and `swim chain export` copies the entries; `swim verify` checks against them (anchored). An anchor only helps if someone other than the operator holds it, so swim doesn't pick one.
- **Cross-session claims?** Yes, as an advisory lease: `swim new` claims the lane until it runs, is archived or stubbed, or the lease expires. `Owner` stays authorship. See [features/lane-claims.md](features/lane-claims.md) (proposed).
- **Do approvals outlive a policy change, and must standing approvals expire?** A rule change stales them; membership is read live; standing approvals expire. See [Approval lifecycle](#approval-lifecycle).

### Resolved from v1

- **CLI or copyable files?** A CLI: one Go binary.
- **Configurable lane count?** It was (config `lanes`); now swim shows the lanes in use, found on disk, and `lanes:` is ignored.
- **Machine-readable summary?** Yes: versioned YAML for every read command.
- **Built-in archive and stub?** Yes: `swim archive` and `swim stub`.
- **Guard registry?** The `Guards` header, guards found from `guard` calls, `swim lint` and `lookup.yml` rules.
- **Scheduling, retries, a UI?** Shipped beyond the v1 scope: the queue, per-step retries, the live view and the hub.
