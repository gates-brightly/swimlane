# Trust model

What swim protects against, what it doesn't, and the test behind each claim.

## The line

**Approval, signing and the chain prove who decided what. They don't make a
script safe.** An approved lane does whatever it says, with the access of
whoever runs it. swim is a tool one person runs; it doesn't sandbox lanes
or isolate one user's work from another's.

**A lane can do what you can.** It runs as the operator: it can read your
files, use your ssh-agent, change its own environment and write `.swim/`.
Nothing here confines a hostile lane; against one, swim mostly detects
(the run attestation and `swim verify` show an approval `swim run` didn't
record). The one thing swim keeps from lanes is the run's signing key
([below](#the-runner-key)). Real confinement means running guarded steps
as another OS user, with credentials the lane never sees; swim doesn't.

## Actors and where credentials live

| Actor | What it does | What it holds |
|---|---|---|
| **Planner** (the AI session) and its **workers** | Write lane scripts, queue batches, read logs. | Nothing from swim. If it runs as your OS user it can reach what you can: isolating it (another user, a container) is up to you. |
| **Operator** | Runs `swim run` / `swim all`, answers guards. | Their shell's environment, which lanes inherit. |
| **Watcher** (`swim watch`) | Runs queued batches. | Its environment's credentials, which never approve a guard: only the batch's `--approve` does. |
| **Runner key** (`signing_key`, default `~/.ssh/id_ed25519`) | Vouches for each run's run key, and signs what swim records outside a run. | An SSH key on the machine that runs rounds: a file, in ssh-agent, or on a security key. |
| **Run key** | Signs every chain entry of one `swim run`. | The launcher's memory alone, for the run. |

## Authorization

*Nothing that needs approval runs without approval for that exact
operation.* How a guard is decided (the flag from `swim run`'s own
environment, a batch's `--approve`, the operator's answer, else a dry run)
is in [guards](guards.md#how-a-guard-is-decided). There is no switch that
approves every guard.

**It fails closed.** No one is asked in CI, without a terminal, under
`--no-prompt` or `--yaml`, or for a queued batch without `swim watch`'s
screen. A question is declined when the approval socket can't be reached,
when the run or lane is stopping, or when the round's `Timeout` runs out.
`confirm` and gates stop the round when there's no answer.

**The guard is advisory.** `guard` runs in the lane's own process. A
script that never calls it, or runs the command outside the `if`, runs the
command; `swim lint` warns (`destructive`). What's enforced outside the
script: a repo's `blocked_commands` (none by default; refused before the
lane starts and at each step), and a running lane can't be rerun, edited
or archived.

**Under `swim run`, the launcher decides and records:**

- Each lane process gets its own token, bound to its process tree where
  the OS can tell (Linux, macOS). The launcher, not the request, says
  which lane, round and manifest ask; it refuses flags the header doesn't
  declare, decides from its own environment as the run started, and shows
  the lane's description labelled as the lane's.
- It writes each approve entry itself, when the lane uses its one-time
  approval id. `swim _mark APPROVED` without one is refused (`a lane can't
  approve itself`), as is an id given to another lane or used already.
- swim's hidden commands (`_start`, `_mark`, `_stage`, `_finish`) can't
  act for another lane (`lane 1 can't report for lane 2`).
- When its last lane exits, the launcher appends a `run` entry: what each
  lane ran and every approval it recorded. `swim verify` fails on an
  approve entry its run didn't record.

(`internal/launcher`, `internal/approval`; outside `swim run`,
`internal/cli/approve.go`.)

**What's still possible, and how it shows.** A lane can unset
`SWIM_LAUNCHED` and decide its guards as if run by hand (a flag it sets
itself then approves); skip `guard`
altogether; append an approve entry to the chain, unsigned or signed by a
key of its own (both fail `swim verify`) or by the runner key if it can
reach it (reported as an approval the run didn't record); or write
`status.yml`, its own log, or entries under a made-up run id (noted as
runs without an attestation). A process that leaves its lane's tree can't
ask or report at all.

## Integrity

*What runs matches what was approved.*

- **The approved bytes are the bytes that run.** Before a lane waits for
  anything, swim pins its [manifest](manifest.md) (script, `# Includes:`,
  sidecar, runner) and stages a read-only copy under `.swim/runs/<run>/<N>/`,
  which is the lane's working directory. Edits made while it waits or runs
  don't reach it; the copy is checked again right before the process
  starts. A bash lane that sources a repo file its header doesn't list
  stops. `# Workspace: live` opts out, in the manifest and with a lint
  warning.
- **A queued batch is pinned:** each lane to its round, manifest and env
  files' hash, so `--approve` covers exactly what was queued. Anything
  changed since is skipped, not run.
- **What ran is kept:** each round's chain entry holds the script's hash,
  and a copy is kept under `.swim/scripts/<sha256>.sh`.
- **Not covered:** env files are pinned per run and batch but aren't in the
  manifest hash (they're per machine); the runner binary is recorded, not
  hashed; external binaries and remote state are never covered.

## Attribution

*Every round traces back to its script, approvals and outcome.* The
[chain](chain.md) records each approval (with how it was given: `prompt`,
`preapproved`, `flag`, `queue`) and each round, hash-linked and signed;
`.swim.log` records every top-level action. What a flag approved is also
shown where results are read: `swim status` (`flag_approved`) and `swim
ci`'s summary and annotations. Nothing proves who set a flag beyond the
operator's user name and key.

**What `swim verify` proves:** every link, every signature against the key
recorded in that entry, every round's log section, kept script and pinned
copy, and each run against its attestation. Unsigned entries at the end
fail unless `--allow-unsigned`; `--force` records acknowledged gaps in a
signed `amend` entry, never changed content. **What it can't:** a chain
replaced wholesale and re-signed with another key, or cut short at its
end, verifies too: nothing outside the machine holds a copy. And it says
nothing about whether the scripts were safe.

## The runner key

*A lane can't get a signature `swim verify` trusts from swim, and with a
runner key that asks for each use, not without asking you.*

As a run starts, the launcher makes an ed25519 run key in memory and has
the runner key sign a delegation to it (run id, key, host, time). Every
entry of the run is signed with the run key and carries the delegation;
both check with plain `ssh-keygen -Y verify`. The run key is never
written, put in a lane's environment or left on a descriptor a lane
inherits. A lane's round entry goes through the launcher (`swim _finish`
over the approval socket), which signs only that lane's own round entry,
under the lane, round id, run and manifest it started it with. On Linux the
launcher is non-dumpable, so a process of the same user can't read its
memory (root can).

What's left is the runner key itself, which a lane can use as you can:

| Where the runner key lives | What a lane can do |
|---|---|
| A file with no passphrase | Read it and sign anything, during the run or after. |
| A file with a passphrase, through ssh-agent | Ask the agent to sign while it holds the key. |
| Only in ssh-agent (`signing_key` is the `.pub`) | The same; nothing to copy. |
| ssh-agent, added with `ssh-add -c` | Ask the agent, which asks you: swim run's one question as the run starts is expected, any other isn't swim's. |
| A security key (`ed25519-sk`) | Ask the device, which needs your touch: swim run asks once per run. |

So keep the runner key behind a question (`ssh-add -c` or a security key).
If it won't vouch for a run key, `signing: required` stops the run before
any lane starts and `auto` records the run unsigned, with one warning. A
watcher nobody watches has no one to touch a key, so its key is in one of
the weaker modes. A round entry's contents (its result, its log's hash)
are what the lane reports; the attestation checks what each lane ran, not
how it ended.

## Out of scope

- **A malicious or mistaken script, approved and run with broad
  credentials.** Limit the damage outside swim: scoped, short-lived
  credentials, and watchers as a separate OS user, in a container or VM.
- **An AI that shares your OS user.**
- **Whoever holds the runner key,** or can rewrite the chain wholesale or
  cut it short.

## Claims and the tests that prove them

Test files are under `internal/`; a bare package name means its own tests.

| Claim | Test |
|---|---|
| With no one to ask, an unset guard is a dry run. | `TestNoPromptWithoutTerminal` (e2e/approval_test.go) |
| `FLAG=1` approves, `FLAG=0` declines without asking, and a prompt answer is logged with who gave it. | `TestGuardAsksForApproval` (e2e/approval_test.go) |
| A stopping lane, or a round whose `Timeout` ran out, declines. | `TestApprovalGivesUpWhenStopped` (e2e/approval_test.go), `TestClientGivesUp` (approval) |
| A stopping run declines the question being asked and every later one. | `TestCancelDeclines` (approval) |
| An unreachable approval socket declines. | `TestGuardDeclinesOnSocketError` (e2e/approval_test.go) |
| An env file can't approve a guard. | `TestEnvFileGuardFlagRefused` (e2e/envfile_test.go) |
| A watcher's own environment approves nothing; a batch's approvals are recorded as the queue's. | `TestQueueAndWatchOnce` (e2e/queue_test.go) |
| A batch approves only what its `--approve` names, credited to who queued it. | `TestWatchApprovesOnlyTheBatchs` (e2e/queue_test.go) |
| What a guard flag approved shows in `swim status` and the CI summary and annotations. | `TestFlagApproved` (e2e/approval_test.go), `TestCIGitHub` (e2e/ci_test.go) |
| A lane edited after it was queued is skipped. | `TestQueueAndWatchOnce` (e2e/queue_test.go) |
| A lane whose env files changed after it was queued is skipped. | `TestEnvFileQueuePin` (e2e/envfile_test.go) |
| The bytes approved are the bytes that run, even if the file changes mid-run. | `TestManifestStagedBytesRun`, `TestManifestMidRunEdit` (e2e/manifest_test.go) |
| A queued batch's approval covers sourced files and the runner: a change after it was queued skips the lane. | `TestManifestApprovalBinding`, `TestManifestRunnerBinding` (e2e/manifest_test.go) |
| A queued lane whose manifest changed is skipped. | `TestManifestQueuePin` (e2e/manifest_test.go) |
| The broker names the lane that asks; an approval is used once, by its lane. | `TestManifestRequestsFromTheLane`, `TestManifestResponseReuse` (e2e/manifest_test.go) |
| Under `swim run`, a lane can't approve itself: `swim _mark APPROVED` is refused, and an entry written by a lane that hid from swim run fails `swim verify`. | `TestHostileLaneMarksApproved` (e2e/authority_test.go) |
| `FLAG=1` set inside a lane doesn't approve; set in swim run's environment, it does, and the run's attestation lists it. | `TestHostileLaneSetsFlag` (e2e/authority_test.go) |
| The chain names the lane and round id swim run knows, whatever the lane claims; its description is labelled as the lane's. | `TestHostileLaneWrongJob` (e2e/authority_test.go), `TestServerUsesRegisteredLane` (approval) |
| A lane can't mark, stage or finish another lane's round, even with that lane's token. | `TestLaneCantReportForAnother` (e2e/authority_test.go), `TestPeerOutsideTheLane` (approval) |
| The approval socket declines oversized, malformed and slow requests, made-up tokens and lanes that hang up. | `TestBrokerHostileClients` (e2e/authority_test.go), `TestHostileRequests`, `TestDisconnectCancels` (approval) |
| A planted secret (a variable `secret_env_auto` names, a multi-line one, a `secret_env` variable), printed raw, base64- or URL-encoded, split across writes, in labels, guard descriptions, notes and errors, is in no file swim writes, no output it prints and no process's command line; a file no known writer accounts for fails the test. | `TestNoSecretLeaks` (e2e/leak_test.go) |
| Changing, removing or reordering a chain entry is detected. | `TestChainLinksAndDetectsTampering` (chain) |
| An edited log or chain entry fails `swim verify`. | `TestChainAndVerify` (e2e/custody_test.go) |
| Every approval is recorded with its method and signed by the runner; the scripts that ran are kept; `signing: required` refuses without a key; `--strict` fails on unsigned entries. | `TestChainRecordsEveryApproval` (e2e/custody_test.go) |
| Under `swim run`, every entry is signed by a run key the runner key vouched for once; a lane has no runner key file to read, its own try at the agent is put to the operator (who says no), and on Linux it can't read the launcher's memory; `swim verify` trusts the run. When the runner key won't vouch for a run key, `signing: required` stops the run before a lane starts and `auto` records it unsigned, asking once. | `TestRunKeyKeepsSigningFromLanes` (e2e/runkey_test.go) |
| The launcher signs only a lane's own round or check entry, once per lane process, under its own lane, round id, run and manifest; any other kind is refused, and so is a token used from outside its lane. | `TestLaneEntry` (launcher), `TestRecordFromTheLane` (approval) |
| A run key's signature checks with plain `ssh-keygen`; its delegation holds only for its run, key and runner key; a delegation copied onto another key fails `swim verify`. | `TestRunKey` (sign), `TestRunKeyEntries` (chain) |
| Entries signed with an old runner key still verify. | `TestRecoveryLostRunnerKey` (e2e/recovery_test.go) |
| A repo's `blocked_commands` are refused in the script and at the step; git itself is not blocked. | `TestBlockedCommands` (e2e/e2e_test.go) |
| `status.yml`, queue entries and kept scripts are replaced atomically and synced to disk with their directory; chain appends are synced. | `TestStateWritesAreDurable`, `TestWriteAtomic` (fsutil) |
| Of several processes claiming one lane at once, exactly one wins; a pid reused by another process doesn't hold a lane. | `TestClaimRace`, `TestRunningChecksStartTime` (lane) |
| A lane killed mid-step is never reported passed nor rerun on its own: the next `swim run` or `swim watch` records the round as interrupted, in the log and the chain. | `TestReapDeadLane` (e2e/recovery_test.go), `TestReapDead` (launcher) |
| A queue claim is orphaned once its watcher is gone: by pid and start time on its host, by an expired heartbeat lease from another. | `TestWorkerLease` (queue) |
| A crash at any chosen point (mid-step, mid-claim, mid-write, mid-append) leaves state that a restart reads correctly: no false success, no silent re-run, a whole `status.yml`, a repairable chain. | `TestFaultStepAfterExit`, `TestFaultQueueClaimed`, `TestFaultWatchMidBatch`, `TestFaultStatusMidWrite`, `TestFaultChainMidAppend` (e2e/fault_test.go) |
| Several `swim watch` processes on one queue run each batch once, and lanes sharing a lock never overlap. | `TestFaultManyWatchers` (e2e/fault_test.go) |
| The scheduler never starts a lane before its `After:` lanes finish, never lets lock holders overlap, and never reports a failure as a pass, on random graphs. | `TestSchedulerProperties` (launcher) |
| A lane's state moves only as the state machine allows; an ended round's result never changes in place. | `TestTransition`, `TestUpdateChecksTransitions` (status) |
