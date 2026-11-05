# Trust model

What swim protects against, what it doesn't, and the test behind each claim.

## The line

**Approval, signing and the chain prove who decided what. They don't make a
script safe.** A lane script that is approved and run does whatever it
says, with whatever access the person or swimmer running it has. swim makes
sure the right people decided to run it and leaves a record that shows
who. Whether the script is a good idea is up to them.

swim is a tool a person runs, who approves its guarded steps. It is not a multi-user executor that isolates one user's work
from another's, and it doesn't sandbox lane scripts.

**Same user means the lane can do what you can.** A lane runs as the
operator, so it can read the operator's files (the runner key if it's a
file), use their ssh-agent, change its own
environment and write `.swim/`. Nothing below confines a hostile lane: it
can skip its guards and write records. What swim offers against that is
mostly detection: the run attestation and `swim verify` show an approval
swim run didn't record. The one thing swim keeps from lanes is
[signing](#the-runner-key): swim run signs with a key a lane never gets,
and with a runner key that asks you for each use, a lane can't sign an
entry `swim verify` trusts without you being asked. Real confinement needs
the guarded steps run by the launcher under another OS user, holding
credentials the lane never sees. swim doesn't do that.

## Actors and where credentials live

| Actor | What it does | What it holds |
|---|---|---|
| **Planner** (the AI session) and **workers** (its subagents) | Write lane scripts, queue batches, read logs back. | Nothing from swim. They don't need cloud credentials, VPN or approval keys. If the AI runs as your OS user on your machine, it can reach whatever that user can: keeping your credentials from it is up to you (a separate user, a container). |
| **Operator** | Runs `swim run` / `swim all`, answers prompts, sets guard flags. | Their shell's environment (cloud profiles, tokens). Lanes run as the operator and inherit it. |
| **Swimmer** (`swim watch`) | Runs queued batches once their lanes are free, each in a process of its own. | Its own environment's credentials. Its environment never approves a guard: only the batch's approvals count. |
| **Runner key** (`signing_key`, default `~/.ssh/id_ed25519`) | Vouches for each `swim run`'s run key, once per run, and signs what swim records outside a run (`swim verify --force`, a signoff). | An SSH key on the operator's or swimmer's machine: a file, in ssh-agent, or on a security key ([below](#the-runner-key)). |
| **Run key** | Signs every chain entry of one `swim run`, the lanes' rounds included. | Only the launcher's memory, for the run. Never on disk, in a lane's environment or a descriptor a lane inherits. |

## Authorization

*Nothing that needs approval runs without valid authorization for that
exact operation.*

A destructive step sits behind `guard FLAG "<action>"`. Under `swim run`
the launcher decides every guard, not the lane (`internal/launcher/authority.go`,
`internal/authority`); a lane run directly with bash decides for itself
(`internal/cli/approve.go`). The guard is approved in the first of these
ways that applies:

1. **The flag.** `FLAG=1` in the environment approves. Under `swim run` it
   must be in the environment `swim run` itself started with (your shell,
   a wrapper script that runs `swim run`, a queued batch's `--approve`): a
   `FLAG=1` the lane sets for itself is declined, `FLAG=1 was set inside
   the lane, not in the environment swim run started with`. `FLAG` set to
   anything else declines without asking. An env file can't set a guard
   flag: the run stops before anything starts.
2. **The person at the terminal.** An answer given ahead of time in the
   live view, or the prompt
   `swim N requires human approval of task: <action>  Y/N [N]`. For a
   queued batch: `y` on `swim watch`'s screen.

There is no switch that approves every guard.

Anything else is a **dry run**: the step is skipped with `SKIP`, and the log
says how to approve it.

**It fails closed.** No one is asked in CI, without a terminal, under
`swim watch`, with
`--no-prompt` or `--yaml`. A question is
declined when the approval socket can't be reached, when the run or the
lane is stopping (Ctrl-C, `swim interrupt`), when the round's `Timeout`
runs out (`internal/approval`). `confirm` and gates stop
the round when there's no answer.

**The guard is advisory.** `guard` is a bash function in the lane's own
process (`swim guard` for other languages). A script that never calls it,
or runs the command outside the `if`, runs the command. `swim lint` warns
(`destructive`: a delete or drop outside an `if guard` block), and nothing
else stops it. What is enforced outside the script:

- **Blocked commands:** a repo's `blocked_commands` are refused in the
  script before it starts and at the step at run time. swim blocks none by
  itself: lanes may run git and gh (push, commit, `gh pr create`), and a
  guard is how an operator decides on one.
- **Running lanes are protected:** swim won't rerun, edit or archive a
  lane that's running.

**An honest lane's approvals are decided and recorded by the launcher**
(under `swim run`):

- **The launcher is the only judge.** Each lane process gets its own
  approval token, bound to its process tree (where the OS can tell: Linux,
  macOS). The launcher, not the request, says which lane, job and manifest
  ask; it refuses guard flags the manifest doesn't declare; it decides from
  its own snapshot of its environment; and it shows the lane's description labelled as the lane's.
- **The launcher writes the record.** It records each approve entry itself,
  when the lane uses its one-time approval id. `swim _mark APPROVED` without
  one is refused (`a lane can't approve itself`), and an id given to
  another lane, or used already, is refused too.
- **A lane reports only for itself.** swim's hidden commands (`_start`,
  `_mark`, `_stage`, `_finish`, `_approve`) ask the launcher
  which lane and run their process belongs to, and refuse to act for
  another (`lane 1 can't report for lane 2`, exit 2). A token read from
  another lane's process is refused: the request comes from outside that
  lane's tree.
- **The run is attested.** When its last lane exits, the launcher appends a
  `run` entry to the chain: what each lane ran and every approval it
  recorded. `swim verify` fails on an approve entry its run didn't record.

**What's still possible, and how it shows:** a lane runs as the operator,
so it can do what the operator can. swim tells a lane swim run started by
its environment (`SWIM_LAUNCHED`), which the lane can change: then its
guards act as outside swim run, and a guard flag it sets itself approves.
It can append an approve entry to the chain itself: unsigned, or signed by
a key of its own (both fail `swim verify`), or by the runner key if it can
reach it ([the runner key](#the-runner-key)), which `swim verify` then
reports as an approval swim run didn't record. It can write
`.swim/status.yml`, its own log, or a chain entry under a run id it made
up (`swim verify` notes runs without an attestation). And it can skip
`guard` altogether (above). A lane process that leaves its lane's tree
(daemonized) can't ask or report at all, and on platforms where swim can't
name a socket's peer the token alone decides.

## Integrity

*What runs matches what was approved.*

- **What runs has an execution manifest.** Before a lane starts, swim
  builds its manifest: the lane script (or a binary), its `# Includes:`
  (the only repo files it may source or import: under `swim run` a bash
  lane that sources one it doesn't list stops), its sidecar and the runner
  the repo names (its `#!` line). The round's chain
  entry names its hash, and a queued batch is pinned to it.
- **The approved bytes are the bytes that run.** The manifest's files are
  staged read-only under `.swim/runs/<run>/<N>/` before the lane waits for
  anything, and the lane runs in the staged copy: that is its working
  directory, so `source`, `bash helper.sh`, `kubectl apply -f k8s/` and
  `python tool.py` read what was pinned, and a file it uses but doesn't pin
  isn't there (`swim lint` warns, `workspace-file`, and a step that fails
  for it says which file to add to `# Includes:`). Edits made while it waits or runs don't reach it; the stage
  is checked again right before the process starts. A lane with
  `# Workspace: live` runs in the working tree instead, and reads files no
  one approved; that line is in its manifest, and `swim lint` warns about
  it. An absolute path (`$SWIM_ROOT/x`, `/etc/x`) is read
  where it is.
- **A queued batch is pinned to its scripts.** Each lane is pinned to its
  job, its exact script, its manifest and the hash of its env files, so a
  batch's `--approve` covers what was queued. A lane edited, rewritten,
  re-environed or given another runner or policy after it was queued is
  skipped, not run (`its manifest changed since it was queued`).
- **What ran is kept.** Each round's chain entry holds the script's hash,
  and a copy of the script is kept under `.swim/scripts/<sha256>.sh`.
- **Live-view answers are pinned too.** An answer given ahead of time in the
  live view applies only to the script it was given for, in that run.

**Gaps still open:**

- **What the manifest can't pin:** env files are pinned for the run and for
  queued batches, but they're per machine, so they aren't in the manifest
  hash. The runner's binary is recorded per machine, not hashed. External binaries
  and remote state are never covered; lint's
  `source` check flags a sourced file it can't pin.

## Attribution

*Every run traces back to its job, swimmer, approval and outcome.*

- **The chain.** Every approval and every round is an entry in
  `.swim/chain.jsonl`. Each holds a sequence number and the SHA-256 of the
  entry before it, and is signed (`ssh-keygen -Y`, the format git uses for
  SSH commit signing): under `swim run` by the run's run key, with the
  runner key's delegation beside it; otherwise by the runner key itself.
  Changing, removing or reordering an entry breaks every link after it.
- **How each guard was approved.** An approve entry records the guard, the
  action, the script's hash, who approved it (the operator, with the
  runner key), and the method: `prompt`, `preapproved` (answered ahead in
  the live view), `flag` (`FLAG=1`), `queue` (a batch's `--approve`). Entries an older swim recorded may say
  `auto` (its `-y`), `signature`,
  `standing`, `signature+standing`, `hub-signed` or `hub-button`; they
  verify by their own signature like any entry. A round entry
  records the result, the exit code, the hash of its log section, its env
  hash, and the approve entries it used.
- **What a run did.** Once its last lane exits, `swim run` appends a `run`
  entry: the manifest and script each lane ran, every approve entry the
  launcher recorded, and the guard flags its own environment approved.
- **`.swim.log`** records every top-level action, including approvals,
  declines and queue events.
- **Signing modes.** `signing: required` refuses to start a round without a
  key. With `signing: auto` (the default), entries are recorded unsigned
  when no key loads, and with a warning when the key loads but can't sign
  (a passphrase and no agent). `signing: off` records unsigned entries.
  `swim verify` fails on unsigned entries after the last signed one, as
  UNSIGNED (nothing vouches for them, and a chain rewritten with no key
  looks the same) unless `--allow-unsigned`. Unsigned entries a later
  signed entry links to are a warning: that signature covers them. BROKEN
  is kept for a link, hash or signature that doesn't match. `swim run`
  warns when the runner key loads but won't vouch for a run key.
- `swim verify --force` acknowledges gaps and deletions (entries removed
  or out of order, logs and kept scripts deleted) in a signed `amend`
  entry; verify then reports them as AMENDED, a warning that names who
  signed for them, which is not whoever recorded the entries. It never
  amends changed content, and anything after the amendment is checked as
  before. An amendment says someone accepted the gap, not what was in it.

## The runner key

*A lane can't get a signature `swim verify` trusts from swim, and with a
runner key that asks for each use, not without asking you.*

- **swim run signs with a run key.** As a run starts, the launcher makes
  an ed25519 key in its own memory and has the runner key sign a
  statement vouching for it for that run (the run id, the key, the host,
  the time). Every entry of the run is signed with the run key and carries
  that delegation (`run_key`). The run key is never written anywhere, put
  in a lane's environment, or left on a descriptor a lane inherits, and
  swim drops it when the run ends.
- **A lane's round goes through the launcher.** `swim _finish` sends its
  round's entry over the run's approval socket, and the launcher signs it:
  one round (or check) entry per lane process, under the lane, job, run
  and manifest the launcher started it with, whatever the entry says. It
  signs no other kind for a lane (approve entries are its own, as an
  approval is consumed; so is the attestation), and refuses a token used
  from outside its lane's process tree (where the OS can tell). So a lane
  needs no key, and swim gives it none.
- **The launcher's memory is closed to lanes on Linux:** it makes itself
  non-dumpable, so a process of the same user can't ptrace it or read its
  `/proc/<pid>/mem` or `environ` (a process with `CAP_SYS_PTRACE`, root
  included, still can). Elsewhere it relies on the OS's own rules for
  debugging another process.
- **`swim verify` checks the delegation:** each run-key
  signature, and the runner key's signature over the delegation, and that
  the delegation names the key that signed; a delegation copied onto
  another key breaks. Both signatures check with plain `ssh-keygen -Y
  verify`.

What's left is the runner key itself. A lane runs as you, so it can use
the runner key the way you can, and what it signs with it is trusted:

| Where the runner key lives | What a lane can do |
|---|---|
| A file with no passphrase | Read it and sign anything, during the run or after. |
| A file with a passphrase, used through ssh-agent | Ask the agent to sign, while it holds the key. |
| Only in ssh-agent (`signing_key` is the `.pub`) | Ask the agent to sign, while it holds the key; nothing to copy. |
| ssh-agent, added with `ssh-add -c` | Ask the agent, which asks you: swim run's one question as the run starts is expected, any other during a run isn't swim's. |
| A security key (`ssh-keygen -t ed25519-sk`) | Ask the device, which needs your touch (unless made with `no-touch-required`): swim run asks once per run. |

Removing `SSH_AUTH_SOCK` from a lane's environment wouldn't change this
(a process running as you can find the agent's socket) and would break
lanes that use ssh, so swim leaves it. Keep the runner key off disk and
behind a question: an agent with `ssh-add -c` or a security key. Then
swim run asks once per run, and a lane can only get a signature by asking
you. A file key keeps working, as the weaker mode.

If the runner key won't vouch for a run key (you said no, no touch, a
passphrase and no agent), `signing: required` stops the run before any
lane starts; `auto` records the run unsigned, with one warning, instead
of asking again for every entry. A swimmer nobody watches (`swim watch`
in tmux or CI) has no one to touch a key, so its runner key is in one of
the weaker modes.

What this doesn't cover: a round entry's contents (its result, its log's
hash) are what the lane reports, signed by the launcher; a lane can call
`swim _finish` early to report first. The attestation checks what each
lane ran, not how it ended. Commands you run yourself (`swim verify --force`, `swim chain
repair`) sign with the runner key directly,
as does swim run ending the round of a lane a dead run left behind, and
so does a lane that runs them, if it can reach the key.

**What `swim verify` proves, and what it doesn't.** It prints one answer,
VERIFIED or NOT VERIFIED (exit 1). It checks every link, every signature
against the key recorded **in that same entry**, every round's log
section, kept script and staged copy; and each attested run against its
attestation: an approve entry the run didn't record (or that two
attestations claim), or a round whose script or manifest isn't the one it
ran, is BROKEN. Runs that share an id (`--run-id`) each attest their own
lanes. Runs without an attestation (lanes run directly with bash, runs
from before attestations, a `swim run` that didn't finish) are noted. It
fails on anything BROKEN, and on unsigned entries at the end (unless
`--allow-unsigned`); `--yaml` gives the result as data.

What it can't catch: a chain replaced wholesale and re-signed with
another key, or with entries removed from its end, verifies too: nothing
outside the machine holds a copy to check it against. And it doesn't
prove the scripts were safe.

`.swim/chain.jsonl` isn't committed: it lives on the machine that ran the
rounds, with the scripts they ran in `.swim/scripts/`.

## Bypasses, and how each is recorded

These skip the question on purpose. Each leaves a record.

| Bypass | What it does | Recorded as |
|---|---|---|
| `FLAG=1 swim run N` | Approves that guard without asking (scripts, CI). Only from the environment `swim run` started with: a lane that sets `FLAG=1` for itself is declined. | `APPROVED <action> (FLAG=1)` in the log; chain method `flag`, recorded by swim run, attributed to the operator's user name and signed with the runner key; the run's attestation lists the flag. Nothing proves who set the flag beyond that. |
| `swim queue N --approve FLAG` | Approves that guard for one batch. | `(FLAG=1, approved in queue q-...)`; chain method `queue`. |

What a flag approved (`FLAG=1`, a batch's `--approve`) is also loud where
a run's result is read, so it shows without reading the chain: `swim
status` lists the guards under the lane (`flag_approved` in
`status.yml`), and `swim ci` names them in the job summary with a warning
annotation.

## Out of scope

- **A malicious or mistaken script, approved and run with broad
  credentials.** Guards and lint are conventions, not a sandbox: the lane
  runs as you, with your environment, your secrets and your key. Limit the
  damage outside swim: scoped, short-lived credentials for each kind of
  change, and running swimmers as a separate OS user, in a container or a
  VM.
- **An AI that shares your OS user.** swim gives the planner nothing, but it
  doesn't stop a process running as you from using what you have.
- **Tampering by whoever holds the runner key,** or can rewrite the chain
  wholesale or cut it short (above). Lanes run as
  the operator, so a lane can use the runner key wherever you can without
  being asked ([a file, an agent without `ssh-add -c`](#the-runner-key)):
  what it signs then is detectable through the run attestation
  (above), not prevented.

## Claims and the tests that prove them

Test files are under `internal/`. "Pending" names the task in
[dev/tasks.md](../../dev/tasks.md) that will add the proof.

| Claim | Test |
|---|---|
| With no one to ask, an unset guard is a dry run. | `TestNoPromptWithoutTerminal` (e2e/approval_test.go) |
| `FLAG=1` approves, `FLAG=0` declines without asking, and a prompt answer is logged with who gave it. | `TestGuardAsksForApproval` (e2e/approval_test.go) |
| A stopping lane, or a round whose `Timeout` ran out, declines. | `TestApprovalGivesUpWhenStopped` (e2e/approval_test.go), `TestClientGivesUp` (approval) |
| A stopping run declines the question being asked and every later one. | `TestCancelDeclines` (approval) |
| An unreachable approval socket declines. | `TestGuardDeclinesOnSocketError` (e2e/approval_test.go) |
| An env file can't approve a guard. | `TestEnvFileGuardFlagRefused` (e2e/envfile_test.go) |
| A swimmer's own environment approves nothing; a batch's approvals are recorded as the queue's. | `TestQueueAndWatchOnce` (e2e/queue_test.go) |
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
| The chain names the lane and job swim run knows, whatever the lane claims; its description is labelled as the lane's. | `TestHostileLaneWrongJob` (e2e/authority_test.go), `TestServerUsesRegisteredLane` (approval) |
| A lane can't mark, stage or finish another lane's round, even with that lane's token. | `TestLaneCantReportForAnother` (e2e/authority_test.go), `TestPeerOutsideTheLane` (approval) |
| The approval socket declines oversized, malformed and slow requests, made-up tokens and lanes that hang up. | `TestBrokerHostileClients` (e2e/authority_test.go), `TestHostileRequests`, `TestDisconnectCancels` (approval) |
| A planted secret (a variable `secret_env_auto` names, a multi-line one, a `secret_env` variable), printed raw, base64- or URL-encoded, split across writes, in labels, guard descriptions, notes and errors, is in no file swim writes, no output it prints and no process's command line; a file no known writer accounts for fails the test. | `TestNoSecretLeaks` (e2e/leak_test.go) |
| Changing, removing or reordering a chain entry is detected. | `TestChainLinksAndDetectsTampering` (chain) |
| An edited log or chain entry fails `swim verify`. | `TestChainAndVerify` (e2e/custody_test.go) |
| Every approval is recorded with its method and signed by the runner; the scripts that ran are kept; `signing: required` refuses without a key; `--strict` fails on unsigned entries. | `TestChainRecordsEveryApproval` (e2e/custody_test.go) |
| Under `swim run`, every entry is signed by a run key the runner key vouched for once; a lane has no runner key file to read, its own try at the agent is put to the operator (who says no), and on Linux it can't read the launcher's memory; `swim verify` trusts the run. When the runner key won't vouch for a run key, `signing: required` stops the run before a lane starts and `auto` records it unsigned, asking once. | `TestRunKeyKeepsSigningFromLanes` (e2e/runkey_test.go) |
| The launcher signs only a lane's own round or check entry, once per lane process, under its own lane, job, run and manifest; any other kind is refused, and so is a token used from outside its lane. | `TestLaneEntry` (launcher), `TestRecordFromTheLane` (approval) |
| A run key's signature checks with plain `ssh-keygen`; its delegation holds only for its run, key and runner key; a delegation copied onto another key fails `swim verify`. | `TestRunKey` (sign), `TestRunKeyEntries` (chain) |
| Entries signed with an old runner key still verify. | `TestRecoveryLostRunnerKey` (e2e/recovery_test.go) |
| A repo's `blocked_commands` are refused in the script and at the step; git itself is not blocked. | `TestBlockedCommands` (e2e/e2e_test.go) |
| `status.yml`, queue entries and kept scripts are replaced atomically and synced to disk with their directory; chain appends are synced. | `TestStateWritesAreDurable`, `TestWriteAtomic` (fsutil) |
| Of several processes claiming one lane at once, exactly one wins; a pid reused by another process doesn't hold a lane. | `TestClaimRace`, `TestRunningChecksStartTime` (lane) |
| A lane killed mid-step is never reported passed nor rerun on its own: the next `swim run` or `swim watch` records the round as interrupted, in the log and the chain. | `TestReapDeadLane` (e2e/recovery_test.go), `TestReapDead` (launcher) |
| A queue claim is orphaned once its swimmer is gone: by pid and start time on its host, by an expired heartbeat lease from another. | `TestWorkerLease` (queue) |
| A crash at any chosen point (mid-step, mid-claim, mid-write, mid-append) leaves state that a restart reads correctly: no false success, no silent re-run, a whole `status.yml`, a repairable chain. | `TestFaultStepAfterExit`, `TestFaultQueueClaimed`, `TestFaultWatchMidBatch`, `TestFaultStatusMidWrite`, `TestFaultChainMidAppend` (e2e/fault_test.go) |
| Several `swim watch` processes on one queue run each batch once, and lanes sharing a lock never overlap. | `TestFaultManyWatchers` (e2e/fault_test.go) |
| The scheduler never starts a lane before its `After:` lanes finish, never lets lock holders overlap, and never reports a failure as a pass, on random graphs. | `TestSchedulerProperties` (e2e/property_test.go) |
| A lane's state moves only as the state machine allows; a finished round's result never changes in place. | `TestTransition`, `TestUpdateChecksTransitions` (status) |
