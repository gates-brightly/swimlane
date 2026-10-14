# Trust model

What swim protects against, what it doesn't, and the test behind each claim.

[← back to the README](../README.md#what-swim-protects-against) ·
[Architecture](architecture.md) · [State machines](state.md) ·
[Recovery](recovery.md)

## The line

**Approval, signing and the chain prove who decided what. They don't make a
script safe.** A lane script that is approved and run does whatever it
says, with whatever access the person or worker running it has. swim makes
sure the right people decided to run it and leaves a record that shows
who. Whether the script is a good idea is up to them.

swim is a tool a person runs, with teammates approving from their own
machines. It is not a multi-user executor that isolates one user's work
from another's, and it doesn't sandbox lane scripts.

## Actors and where credentials live

| Actor | What it does | What it holds |
|---|---|---|
| **Planner** (the AI session) and **workers** (its subagents) | Write lane scripts, queue batches, read logs back. | Nothing from swim. They don't need cloud credentials, VPN or approval keys. If the AI runs as your OS user on your machine, it can reach whatever that user can: keeping your credentials from it is up to you (a separate user, a container). |
| **Operator** | Runs `swim run` / `swim all`, answers prompts, sets guard flags. | Their shell's environment (cloud profiles, tokens). Lanes run as the operator and inherit it. |
| **Watch worker** (`swim watch`) | Runs queued batches when swim is idle. | Its own environment's credentials. Its environment never approves a guard: only the batch's approvals count. |
| **Runner key** (`signing_key`, default `~/.ssh/id_ed25519`) | Signs every chain entry swim records on this machine. | An SSH key on the operator's or worker's machine (or in ssh-agent). |
| **Approvers** | Sign approvals of exact scripts, or standing approvals, with `swim approve`. | Their own SSH keys. Their public keys are in the committed `lookup.yml`. |
| **Secrets** (`swim secret`) | Encrypted to your SSH key with age, decrypted once per run. | Readable by every lane in the run: they keep values out of logs, the hub and git, not out of lane code. |
| **Hub** (`swim hub`) | Shows workers' status, logs and queues. | Tokens, and only ciphertext for secret grants. Plain HTTP today (T16, pending). |

## Authorization

*Nothing that needs approval runs without valid authorization for that
exact operation.*

A destructive step sits behind `guard FLAG "<action>"`. The guard approves
it in the first of these ways that applies (`internal/assets/lib.sh`,
`internal/cli/approve.go`):

1. **The flag.** `FLAG=1` in the environment approves. `FLAG` set to
   anything else declines without asking. An env file can't set a guard
   flag: the run stops before anything starts. A flag from a deprecated
   `.lane.N.rc` is ignored.
2. **Signed approvals.** Approvals of this exact script (by SHA-256), plus
   the standing approvals of whoever signed the queue entry the run came
   from, are counted against `lookup.yml`'s rule for the guard. A signature
   from a key `lookup.yml` doesn't list, a revoked approval and an expired
   one don't count. An approval bound to evidence counts only while this
   run's evidence matches (`swim watch` doesn't compare evidence, and says
   so in the log).
3. **The person at the terminal.** An answer given ahead of time in the
   live view, or the prompt
   `swim N requires human approval of task: <action>  Y/N [N]`.
4. **`-y`.** Every guard still unanswered is approved, and logged as `-y`.

Anything else is a **dry run**: the step is skipped with `SKIP`, and the log
says how to approve it.

**It fails closed.** No one is asked in CI, without a terminal, under
`swim watch`, with `--no-prompt` or `--yaml`, or with `approvals: flags`.
A question is declined when the approval socket can't be reached, when
the run or the lane is stopping (Ctrl-C, `swim interrupt`), and when the
round's `Timeout` runs out (`internal/approval`). `confirm` and gates stop
the round when there's no answer.

**The guard is advisory.** `guard` is a bash function in the lane's own
process (`swim guard` for other languages). A script that never calls it,
or runs the command outside the `if`, runs the command. `swim lint` warns
(`destructive`: a delete or drop outside an `if guard` block), and nothing
else stops it. What is enforced outside the script:

- **Blocked commands:** `git push`, `git commit`, `git pull` and a repo's
  `blocked_commands` are refused in the script before it starts, at the
  step at run time, and by a git shim on each lane's `PATH`.
- **Running lanes are protected:** swim won't rerun, edit, archive or stub a
  lane that's running.
- **The lock:** a swim with other syntax versions than `.swim.lock` refuses
  to run lanes.

**Gaps still open (T3, pending):** a lane runs as the operator, so a
hostile lane could write `approvals/`, `lookup.yml` or `.swim/status.yml`,
call `swim _mark` to record an approval itself, set `FLAG=1` before calling
`guard`, or send a request for another lane on the approval socket (which
has no per-lane token yet). See [tasks.md](../tasks.md) T3 and
[features/execution-manifest.md](../features/execution-manifest.md).

## Integrity

*What runs matches what was approved.*

- **Approvals are bound to the script's SHA-256.** The hash covers the lane
  script (or a lane's group file, or a binary), the files its
  `# Matrix: KEY=@file` reads values from, and the files its `# Includes:`
  globs match. A script edited after approval no longer matches: its guards
  ask or dry-run, the log says the approval is for another version, and
  `swim verify` lists the approval as stale.
- **A queued batch is pinned to its scripts.** Each lane is pinned to its
  job and its exact script, and to the hash of its env files. A lane edited,
  rewritten or re-environed after it was queued is skipped, not run.
- **What ran is kept.** Each round's chain entry holds the script's hash,
  and a copy of the script is kept under `.swim/scripts/<sha256>.sh`.
- **Live-view answers are pinned too.** An answer given ahead of time in the
  live view applies only to the script it was given for, in that run.

**Gaps still open:**

- **The script runs from the working tree (T1, pending).** The runner runs
  the script's path, and bash reads a script as it goes, so an edit made
  while a round runs changes what runs. The kept copy is for audit only.
  For queued runs the pin is checked after locks are taken but before the
  lane waits for a slot.
- **What the hash covers (T2, pending).** It doesn't cover files pulled in
  with `source` that `# Includes:` doesn't list, the `lane.N.yml` sidecar
  itself, the runner chosen by config, or `swim.yml` settings. Env files are
  pinned only for queued runs (and their hash is recorded in the chain).
  External binaries and remote state are never covered.
- **The hash a guard checks** comes from `.swim/status.yml`, which a lane
  can write (T3, pending).

See [features/execution-manifest.md](../features/execution-manifest.md).

## Attribution

*Every run traces back to its job, worker, approval and outcome.*

- **The chain.** Every approval and every round is an entry in
  `.swim/chain.jsonl`. Each holds a sequence number and the SHA-256 of the
  entry before it, and is signed with the runner's SSH key
  (`ssh-keygen -Y`, the format git uses for SSH commit signing). Changing,
  removing or reordering an entry breaks every link after it.
- **How each guard was approved.** An approve entry records the guard, the
  action, the script's hash, the approvers, and the method: `signature`,
  `standing` or `signature+standing` (signed approvals, each carrying the
  approver's own signature), `prompt`, `preapproved` (answered ahead in the
  live view), `flag` (`FLAG=1`), `queue` (a batch's `--approve`) or `auto`
  (`-y`). A round entry records the result, the exit code, the hash of its
  log section, its env hash, and the approve entries it used.
- **Who queued a batch** is proven by the queuer's SSH signature on the
  entry. Standing approvals apply only when that signature checks out
  against a key in `lookup.yml` and the entry holds the lane's exact script.
- **`.swim.log`** records every top-level action, including approvals,
  declines and queue events.
- **Signing modes.** `signing: required` refuses to start a round without a
  key. With `signing: auto` (the default), entries are recorded unsigned
  when no key loads, and with a warning when the key loads but can't sign
  (a passphrase and no agent). `signing: off` records unsigned entries.
  `swim verify` warns about unsigned entries, and `--strict` fails on them.

**What `swim verify` proves, and what it doesn't.** It checks every link,
every signature, every round's log section, every kept script and every
signed approval. But it checks each entry's signature against the public
key stored **in that same entry** (`internal/chain/chain.go`). Nothing ties
that key to `lookup.yml` yet: a signer `lookup.yml` doesn't list is only a
warning (`--strict` fails on it), and `lookup.yml` sits in the repo, where
a lane can write it. So today `swim verify` can't detect:

- entries removed from the end of the chain;
- a chain replaced wholesale and re-signed with another key;
- a chain rewritten by anyone holding the runner key.

`.swim/chain.jsonl` isn't committed, so it lives only on the machine that
ran the rounds. Pinned signers and external checkpoints are pending: T8,
[features/chain-anchors.md](../features/chain-anchors.md).

## Bypasses, and how each is recorded

These skip the question on purpose. Each leaves a record.

| Bypass | What it does | Recorded as |
|---|---|---|
| `FLAG=1 swim run N` | Approves that guard without asking (scripts, CI). | `APPROVED <action> (FLAG=1)` in the log; chain method `flag`, attributed to the operator's user name and signed with the runner key. Nothing proves who set the flag beyond that. |
| `swim queue add --approve FLAG` | Approves that guard for one batch. | `(FLAG=1, approved in queue q-...)`; chain method `queue`. |
| `swim run -y` / `swim all -y` | Approves every guard whose flag isn't set. `FLAG=0` still declines. | `approved by swim run -y (<user>)`; chain method `auto`; `.swim.log` says `with swim run -y`; the `--yaml` stream has an `approved` event with `(-y)`. |
| `swim queue add -y` | The same, for one batch. | `approved by queue q-... -y (<queuer>)`; chain method `auto`. |
| `swim watch -y` | The worker approves every guard in every batch, beyond what each batch approves. | `approved by swim watch -y (<user>)`; chain method `auto`; the consumed entry's `approved_by`. |
| `swim watch --approved` | Queues and runs a script as soon as its signed approvals meet `lookup.yml`'s rules, with no one choosing when. | A queue entry by `approval (<names>)` and a `.swim.log` queue event; the guards are approved by `signature`. |

`-y` isn't yet surfaced in `swim status`, CI summaries or the hub (T5,
pending): today you find it in the log, `.swim.log` and the chain.

## Out of scope

- **A malicious or mistaken script, approved and run with broad
  credentials.** Guards and lint are conventions, not a sandbox: the lane
  runs as you, with your environment, your secrets and your key. Limit the
  damage outside swim: scoped, short-lived credentials for each kind of
  change, and running workers as a separate OS user, in a container or a
  VM. Releasing a credential only after its guard is approved is designed in
  [features/guarded-secrets.md](../features/guarded-secrets.md) (pending).
- **An AI that shares your OS user.** swim gives the planner nothing, but it
  doesn't stop a process running as you from using what you have.
- **Tampering by whoever holds the runner key,** or truncation of the chain,
  until T8 lands (above).
- **The hub's transport.** It serves plain HTTP with shared tokens; put it
  behind a TLS proxy (T16, pending).

## Claims and the tests that prove them

Test files are under `internal/`. "Pending" names the task in
[tasks.md](../tasks.md) that will add the proof.

| Claim | Test |
|---|---|
| With no one to ask, an unset guard is a dry run. | `TestNoPromptWithoutTerminal` (e2e/approval_test.go) |
| `FLAG=1` approves, `FLAG=0` declines without asking, and a prompt answer is logged with who gave it. | `TestGuardAsksForApproval` (e2e/approval_test.go) |
| A stopping lane, or a round whose `Timeout` ran out, declines. | `TestApprovalGivesUpWhenStopped` (e2e/approval_test.go), `TestClientGivesUp` (approval) |
| A stopping run declines the question being asked and every later one. | `TestCancelDeclines` (approval) |
| An unreachable approval socket declines. | `TestGuardDeclinesOnSocketError` (e2e/trust_test.go) |
| `-y` approves only unset guards, and is logged as `-y`. | `TestAutoApprove` (e2e/approval_test.go) |
| An env file can't approve a guard. | `TestEnvFileGuardFlagRefused` (e2e/envfile_test.go) |
| Signed approvals must meet `lookup.yml`'s rule; an unknown key doesn't count; a revoked approval stops counting. | `TestSignedApprovalAndVerify` (e2e/custody_test.go) |
| Standing approvals apply only to batches the queuer signed, holding the exact script. | `TestStandingApproval` (e2e/custody_test.go) |
| A worker's own environment approves nothing; a batch's approvals are recorded as the queue's. | `TestQueueAndWatchOnce` (e2e/queue_test.go) |
| `swim watch -y` is logged as the worker's. | `TestWatchYes` (e2e/queue_test.go) |
| `swim watch --approved` runs a script once it is fully approved. | `TestWatchSigned` (e2e/custody_test.go) |
| A script edited after approval no longer matches its approval. | `TestSignedApprovalAndVerify` (e2e/custody_test.go) |
| A lane edited after it was queued is skipped. | `TestQueueAndWatchOnce` (e2e/queue_test.go) |
| A lane whose env files changed after it was queued is skipped. | `TestEnvFileQueuePin` (e2e/envfile_test.go) |
| The bytes approved are the bytes that run, even if the file changes mid-run. | Pending T1 |
| An approval covers sourced files, the sidecar, the runner and policy. | Pending T2 |
| A lane can't approve itself. | Pending T3, T25 |
| Changing, removing or reordering a chain entry is detected. | `TestChainLinksAndDetectsTampering` (chain) |
| An edited log or chain entry fails `swim verify`. | `TestSignedApprovalAndVerify` (e2e/custody_test.go) |
| Every approval is recorded with its method and signed by the runner; the scripts that ran are kept; `signing: required` refuses without a key; `--strict` fails on unsigned entries. | `TestChainRecordsEveryApproval` (e2e/custody_test.go) |
| Entries signed with an old runner key still verify. | `TestRecoveryLostRunnerKey` (e2e/recovery_test.go) |
| `git push`, `commit` and `pull` are blocked in the script, at the step and by the shim. | `TestBlockedCommands` (e2e/e2e_test.go) |
| A truncated chain, or one re-signed with a new key, is detected. | Pending T8 |
| A crash never leaves a lane, claim or record half-written. | Pending T6, T7 |
