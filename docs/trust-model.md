# Trust model

What swim protects against, what it doesn't, and the test behind each claim.

[← back to the README](../README.md#what-swim-protects-against) ·
[Architecture](architecture.md) · [State machines](state.md) ·
[Recovery](recovery.md)

## The line

**Approval, signing and the chain prove who decided what. They don't make a
script safe.** A lane script that is approved and run does whatever it
says, with whatever access the person or swimmer running it has. swim makes
sure the right people decided to run it and leaves a record that shows
who. Whether the script is a good idea is up to them.

swim is a tool a person runs, with teammates approving from their own
machines. It is not a multi-user executor that isolates one user's work
from another's, and it doesn't sandbox lane scripts.

**Same user means the lane can do what you can.** A lane runs as the
operator, so it can read the operator's files, including the runner key
and the secrets key, use their ssh-agent, change its own environment and
write `.swim/`. Nothing below confines a hostile lane: it can skip its
guards and forge records signed with the runner key. What swim offers
against that is detection, not prevention: the run attestation and `swim
verify` show an approval swim run didn't record, and anchors show a
history changed after it was checkpointed. Real confinement needs the
guarded steps run by the launcher under another OS user, holding
credentials the lane never sees. swim doesn't do that.

## Actors and where credentials live

| Actor | What it does | What it holds |
|---|---|---|
| **Planner** (the AI session) and **workers** (its subagents) | Write lane scripts, queue batches, read logs back. | Nothing from swim. They don't need cloud credentials, VPN or approval keys. If the AI runs as your OS user on your machine, it can reach whatever that user can: keeping your credentials from it is up to you (a separate user, a container). |
| **Operator** | Runs `swim run` / `swim all`, answers prompts, sets guard flags. | Their shell's environment (cloud profiles, tokens). Lanes run as the operator and inherit it. |
| **Swimmer** (`swim watch`) | Runs queued batches once their lanes are free, each in a process of its own. | Its own environment's credentials. Its environment never approves a guard: only the batch's approvals count. |
| **Runner key** (`signing_key`, default `~/.ssh/id_ed25519`) | Signs every chain entry swim records on this machine. | An SSH key on the operator's or swimmer's machine (or in ssh-agent). |
| **Approvers** | Sign approvals of exact scripts, or standing approvals, with `swim approve`. | Their own SSH keys. Their public keys are in the committed `lookup.yml`. |
| **Secrets** (`swim secret`) | Encrypted to your SSH key with age, decrypted once per run and served to lanes by the run's broker, which knows each lane process by its token. | An unguarded secret is readable by every lane in the run: it keeps values out of logs, the hub and git, not out of lane code. A guarded one (`swim.yml` `secrets:`) goes only to a lane whose guard of it was approved in this run. With a key that has no passphrase, a lane can decrypt the store itself. |
| **Hub** (`swim hub`) | Admits swimmers: any machine may join (`swim auth login`), but it shows nothing and takes no command until the hub's admin admits it, and a swimmer that joined already joins again only with its own token. Shows swimmers' status, logs and queues, and relays hub users' commands (interrupt, drain, run, log, and answers to waiting guards) to headless swimmers. It runs nothing itself and can't make an answer valid. Viewing a non-private hub shows a job's steps, commands and exit codes (masked as logs are); output needs a hub user. | Tokens (only hashes of swimmer and user tokens), sessions (by their ids' hashes), commands, and only ciphertext for secret grants. TLS (`--tls-cert`/`--tls-key`, clients pin `--ca`; session cookies are `Secure` under it); plain HTTP only on loopback or with `--insecure`. |
| **Hub users** (`swim auth users add`) | Send commands to headless swimmers as their role allows: `viewer` fetches logs and sends signed answers; `operator` also interrupts, drains, runs, and answers with a button; `admin` is an operator who also does what the admin token does (users, swimmer approvals, grants), from the CLI with their token or the app's Admin page with their session. | Their own token, or a 12-hour session cookie (`HttpOnly`, `SameSite=Strict`, CSRF token on every form). The admin token manages users and sends no command. An admin session's changes need its CSRF token like any command; it never sends a secret's value (grants are encrypted to the swimmer's key on the admin's machine). |

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
2. **Signed approvals.** Under `swim run`, as `approvals/` and `lookup.yml`
   stood when the run started: files written after that (by a lane, or by
   anyone) don't count until the next run. Approvals of this exact script (by SHA-256), plus
   the standing approvals of whoever signed the queue entry the run came
   from, are counted against `lookup.yml`'s rule for the guard. A signature
   from a key `lookup.yml` doesn't list (read live: a removed key stops
   counting at once), a revoked approval and an expired one don't count,
   and neither does one signed under a different rule for the guard
   (standing approvals expire, 30 days by default, capped by
   `standing.max`). An approval bound to evidence counts only while this
   run's evidence matches (`swim watch` doesn't compare evidence, and says
   so in the log).
3. **The person at the terminal.** An answer given ahead of time in the
   live view, or the prompt
   `swim N requires human approval of task: <action>  Y/N [N]`.
4. **`-y`.** Every guard still unanswered is approved, and logged as `-y`.
5. **An answer through the hub** (`swim watch --headless`, where no one is
   at a terminal and `swim.yml`'s `headless.allow` takes `answer`). The
   question is published in the hub's Approvals inbox and the lane waits up
   to `headless.answer_timeout`. A **signed** answer (`swim approve --hub`,
   or a pasted `ssh-keygen -Y sign`) covers a statement the swimmer builds
   from its own facts: the question's id, the swimmer, lane, job, run,
   script, manifest, evidence, guard, decision and expiry. The swimmer
   checks the signature against `lookup.yml` as the run started and counts
   signers until the guard's rule is met; the launcher checks the result
   again before it grants. A **button** answer (an operator's click)
   counts only under `headless.approvals: hub`, from a user `lookup.yml`
   lists, for a guard no `require:` rule names and that needs no evidence.
   Either kind may decline.

Anything else is a **dry run**: the step is skipped with `SKIP`, and the log
says how to approve it.

**It fails closed.** No one is asked in CI, without a terminal, under
`swim watch` (a headless swimmer asks only through its hub), with
`--no-prompt` or `--yaml`, or with `approvals: flags`. A question is
declined when the approval socket can't be reached, when the run or the
lane is stopping (Ctrl-C, `swim interrupt`), when the round's `Timeout`
runs out (`internal/approval`), and, through the hub, when no answer
comes by `headless.answer_timeout` (the hub gone, nobody answering). `confirm` and gates stop
the round when there's no answer.

**The guard is advisory.** `guard` is a bash function in the lane's own
process (`swim guard` for other languages). A script that never calls it,
or runs the command outside the `if`, runs the command. `swim lint` warns
(`destructive`: a delete or drop outside an `if guard` block), and nothing
else stops it. What is enforced outside the script:

- **Blocked commands:** `git push`, `git commit`, `git pull` and a repo's
  `blocked_commands` are refused in the script before it starts, at the
  step at run time, and by a git shim on each lane's `PATH`.
- **Guarded secrets:** a secret `swim.yml` guards by a flag (`secrets:
  NAME: {guard: FLAG}`) is released by the run's secrets broker only to a
  lane process whose guard of that flag the launcher approved and recorded
  in this run. A script that skips the guard, or sets `FLAG=1` itself,
  gets `refused: NAME is guarded by FLAG, which isn't approved in this
  run`; each release and refusal is a chain entry. This makes a guard
  enforced for credentials that live only in swim's vault, and for nothing
  else: ambient credentials (your shell's `~/.aws`, tokens in the
  environment) are still the lane's, and a released value is the lane's to
  use as it likes. `release: step` hands it out once.
- **Running lanes are protected:** swim won't rerun, edit, archive or stub a
  lane that's running.
- **The lock:** a swim with other syntax versions than `.swim.lock` refuses
  to run lanes.

**An honest lane's approvals are decided and recorded by the launcher**
(under `swim run`):

- **The launcher is the only judge.** Each lane process gets its own
  approval token, bound to its process tree (where the OS can tell: Linux,
  macOS). The launcher, not the request, says which lane, job and manifest
  ask; it refuses guard flags the manifest doesn't declare; it decides from
  its own snapshot of `approvals/`, `lookup.yml` and its environment; and
  it shows the lane's description labelled as the lane's.
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
It can append an approve entry to the chain itself, signed with the runner
key, which it can read; `swim verify` then reports an approval swim run
didn't record. It can write `.swim/status.yml`, its own log, or a chain
entry under a run id it made up (`swim verify` notes runs without an
attestation). And it can skip `guard` altogether (above). The attestation
makes a forged entry detectable, not impossible: keeping the runner key
from lanes (a separate OS user or an agent that confirms each signature)
is a recorded follow-up. A lane process that leaves its lane's tree
(daemonized) can't ask or report at all, and on platforms where swim can't
name a socket's peer the token alone decides.

## Integrity

*What runs matches what was approved.*

- **Approvals are bound to an execution manifest.** Before a lane starts,
  swim builds its manifest: the lane script (or a binary), its `# Includes:`
  (the only repo files it may source or import: under `swim run` a bash
  lane that sources one it doesn't list stops), its sidecar, the
  runner the repo names, the policy part of `swim.yml` and the `lookup.yml`
  rules. `swim approve N` signs the manifest's hash and lists what it pins.
  A change to any of it stales the approval: its guards ask or dry-run, and
  the guard's SKIP, `swim approve` and `swim verify` say what changed.
  Approvals made before manifests still count by script hash.
- **The approved bytes are the bytes that run.** The manifest's files are
  staged read-only under `.swim/runs/<run>/<N>/` before the lane waits for
  anything, and the lane runs in the staged copy: that is its working
  directory, so `source`, `bash helper.sh`, `kubectl apply -f k8s/` and
  `python tool.py` read what was pinned, and a file it uses but doesn't pin
  isn't there. Edits made while it waits or runs don't reach it; the stage
  is checked again right before the process starts. A lane with
  `# Workspace: live` runs in the working tree instead, and reads files no
  one approved; that line is in its manifest, so an approval of it says
  so, and `swim lint` warns about it. An absolute path (`$SWIM_ROOT/x`, `/etc/x`) is read
  where it is.
- **A queued batch is pinned to its scripts.** Each lane is pinned to its
  job and its exact script, and to the hash of its env files. A lane edited,
  rewritten or re-environed after it was queued is skipped, not run.
- **What ran is kept.** Each round's chain entry holds the script's hash,
  and a copy of the script is kept under `.swim/scripts/<sha256>.sh`.
- **Live-view answers are pinned too.** An answer given ahead of time in the
  live view applies only to the script it was given for, in that run.

**Gaps still open:**

- **What the manifest can't pin:** env files are pinned for the run and for
  queued batches, but they're per machine, so they aren't in the signed
  manifest hash: an env-only change doesn't stale a signed approval. The
  runner's binary is recorded per machine, not signed. External binaries,
  remote state and modules found only at run time are never covered; lint's
  `source` check flags a sourced file it can't pin. `# Observe:` facts are
  checked again before the guarded step, and drift ends the round.

## Attribution

*Every run traces back to its job, swimmer, approval and outcome.*

- **The chain.** Every approval and every round is an entry in
  `.swim/chain.jsonl`. Each holds a sequence number and the SHA-256 of the
  entry before it, and is signed with the runner's SSH key
  (`ssh-keygen -Y`, the format git uses for SSH commit signing). Changing,
  removing or reordering an entry breaks every link after it.
- **How each guard was approved.** An approve entry records the guard, the
  action, the script's hash, the approvers, and the method: `signature`,
  `standing` or `signature+standing` (signed approvals, each carrying the
  approver's own signature), `prompt`, `preapproved` (answered ahead in the
  live view), `flag` (`FLAG=1`), `queue` (a batch's `--approve`), `auto`
  (`-y`), `hub-signed` (answers signed through the hub, each approver's
  statement and signature recorded) or `hub-button` (a hub user's click,
  unsigned). A round entry records the result, the exit code, the hash of its
  log section, its env hash, and the approve entries it used.
- **What a run did.** Once its last lane exits, `swim run` appends a `run`
  entry: the manifest and script each lane ran, every approve entry the
  launcher recorded, and the guard flags its own environment approved.
- **Who queued a batch** is proven by the queuer's SSH signature on the
  entry. Standing approvals apply only when that signature checks out
  against a key in `lookup.yml` and the entry holds the lane's exact script.
- **`.swim.log`** records every top-level action, including approvals,
  declines and queue events.
- **Signing modes.** `signing: required` refuses to start a round without a
  key. With `signing: auto` (the default), entries are recorded unsigned
  when no key loads, and with a warning when the key loads but can't sign
  (a passphrase and no agent). `signing: off` records unsigned entries.
  `swim verify` fails on unsigned entries (nothing vouches for them, and a
  chain rewritten with no key looks the same) unless `--allow-unsigned`.

**What `swim verify` proves, and what it doesn't.** It prints one answer,
VERIFIED or NOT VERIFIED (exit 1), and what it couldn't check. It fails
when any of four levels fails or any entry is unsigned; `--levels` shows
each level, and `--yaml` gives each its own field:

- **consistent:** every link, every signature against the key recorded
  **in that same entry**, every round's log section, kept script and staged
  copy, and every signed approval; and each attested run against its
  attestation: an approve entry the run didn't record (or that two
  attestations claim), or a round whose script or manifest isn't the one it
  ran, is a break. Runs that share an id (`--run-id`) each attest their own
  lanes. Runs without an attestation (lanes run directly with bash, runs from
  before attestations, a `swim run` that didn't finish) are noted. A chain
  replaced wholesale and re-signed with another key is consistent too.
- **trusted:** every signature is by a key in `trust.yml` (committed),
  whose key set is pinned outside the repo: in the operator's
  `~/.config/swim/trust-pins.yml` on first use or by `swim keys pin`, or by
  hash in CI (`--trust-pin`, `SWIM_TRUST_PIN`). A change to `trust.yml`
  counts only when signed by a key the pinned set trusts (`swim keys trust
  --sign`, recorded in `trust.yml.sig`); a hand edit fails the level.
  `lookup.yml`'s keys must be in `trust.yml` (`swim lint`).
- **anchored:** the chain still holds the entry every checkpoint at each
  anchor recorded, the oldest as well as the newest: a chain rewritten,
  re-signed, padded past the last checkpoint and checkpointed again still
  fails every checkpoint taken before the rewrite. (An anchor checks only
  that a new checkpoint's seq goes up; it can't see the chain. Anyone who
  can send a checkpoint, a swimmer's own hub token included, can add one,
  but not remove the old ones.) A checkpoint (seq, head hash, time, host, signed by
  the runner) goes to each `chain_anchors` destination (`file:/path`
  outside the repo, or the hub) at the end of every top-level run, CI job
  and watch batch. `swim chain export` also copies the entries, and `swim
  verify --against DEST` checks against them.
- **authorized:** each approve entry met `lookup.yml`'s rule for its guard,
  under the `lookup.yml` it recorded (`lookup_sha256`). Answers signed
  through the hub count like signed approvals (and consistent checks each
  signature and that its statement approves that guard, lane and run); a
  `hub-button` approval fails any `require:` rule that names its guard.

What each level can't catch:

- **trusted** rests on the pin. A lane runs as the operator, so it could
  write `~/.config/swim/trust-pins.yml` directly (not through the repo). A
  pin a lane can't reach is one CI passes from its own settings
  (`--trust-pin`). Trust on first use trusts whatever `trust.yml` says the
  first time; check it, or pin it explicitly.
- **anchored** proves nothing about entries written after the last
  checkpoint, and someone holding the runner key **and** write access to
  every anchor can rewrite everything. Anchors should be held by someone
  other than the operator: another volume mounted append-only, a hub run by
  someone else. Without an anchor, entries removed from the end of the
  chain can't be told from a shorter history (`anchored: n/a`, which
  `--strict` fails).
- **authorized** checks the rules, not the people: a prompt, `FLAG=1`, `-y`
  or queue approval meets any rule except one marked `signed: true`.
- None of them proves the scripts were safe.

`.swim/chain.jsonl` isn't committed; with an anchor, a copy lives outside
the machine that ran the rounds (`swim chain export`).

## Bypasses, and how each is recorded

These skip the question on purpose. Each leaves a record.

| Bypass | What it does | Recorded as |
|---|---|---|
| `FLAG=1 swim run N` | Approves that guard without asking (scripts, CI). Only from the environment `swim run` started with: a lane that sets `FLAG=1` for itself is declined. | `APPROVED <action> (FLAG=1)` in the log; chain method `flag`, recorded by swim run, attributed to the operator's user name and signed with the runner key; the run's attestation lists the flag. Nothing proves who set the flag beyond that. |
| `swim queue add --approve FLAG` | Approves that guard for one batch. | `(FLAG=1, approved in queue q-...)`; chain method `queue`. |
| `swim run -y` / `swim all -y` | Approves every guard whose flag isn't set. `FLAG=0` still declines. | `approved by swim run -y (<user>)`; chain method `auto`; `.swim.log` says `with swim run -y`; the `--yaml` stream has an `approved` event with `(-y)`. |
| `swim queue add -y` | The same, for one batch. | `approved by queue q-... -y (<queuer>)`; chain method `auto`. |
| `swim watch -y` | The swimmer approves every guard in every batch, beyond what each batch approves. | `approved by swim watch -y (<user>)`; chain method `auto`; the consumed entry's `approved_by`. |
| `swim watch --approved` | Queues and runs a script as soon as its signed approvals meet `lookup.yml`'s rules, with no one choosing when. | A queue entry by `approval (<names>)` and a `.swim.log` queue event; the guards are approved by `signature`. |
| `headless.approvals: hub` in `swim.yml` | On a headless swimmer, a hub operator's button click approves a guard that no `lookup.yml` `require:` rule names: the decision rests on the hub's login, as the terminal prompt rests on the operator's. | `approved through the hub by <user> (button, unsigned)`; chain method `hub-button`; the `hub answer` command in `.swim.log` and the chain; `swim doctor` warns (`headless-hub-answers`). |

What `-y` approved is also loud where a run's result is read, so it shows
without reading the chain: `swim status` lists the guards under the lane
(`auto_approved`, `auto_via` in `status.yml`), the round's chain entry
carries `auto_approved` (`swim history`), `swim ci` names them in the job
summary with a warning annotation, and the hub shows a `-y` badge on the
lane and on a swimmer running `swim watch -y`. Guards approved by a flag
(`FLAG=1`, a batch's `--approve`) are listed the same way, as
`flag_approved`.

## Out of scope

- **A malicious or mistaken script, approved and run with broad
  credentials.** Guards and lint are conventions, not a sandbox: the lane
  runs as you, with your environment, your secrets and your key. Limit the
  damage outside swim: scoped, short-lived credentials for each kind of
  change, and running swimmers as a separate OS user, in a container or a
  VM. A guarded secret (above) releases a credential only after its guard
  is approved, but only for credentials that exist nowhere else; and a
  lane can read an unprotected secrets key, or a headless swimmer's own key
  and hub token, as easily as you can.
- **An AI that shares your OS user.** swim gives the planner nothing, but it
  doesn't stop a process running as you from using what you have.
- **Tampering by whoever holds the runner key and can write every anchor,**
  and anything written after the last checkpoint (above). Lanes run as
  the operator, so a lane can read the runner key too: what it signs is
  detectable through the run attestation (consistent, above), not
  prevented.
- **A compromised hub.** It can show false information and send commands,
  as any user could. Each command is checked by the swimmer, not the hub:
  the swimmer's access (`swim auth login --access`: `read` takes only
  log requests, `readwrite` every command; kept on the swimmer, in
  `~/.config/swim/auth.yml` or an older enrollment's `.swim/hub.yml`, so
  the hub can't raise it), then
  `swim.yml`'s `headless.allow` (committed; `run`, `trash`, `pause`, `unpause`,
  `queue`, `dequeue` and `requeue` are off by default), the command's expiry capped by `headless.command_ttl`, its pin
  (the run, the script and manifest, or the lane's job the page showed), and replay state kept on the
  swimmer (the hub's epoch, the last seq and recent command ids). A `run`
  or `queue` from the hub queues an unsigned batch with no approvals of its
  own, and a `requeue` queues a finished batch's lanes again the same way
  (never with the old batch's approvals or signature), and
  a log goes out only masked, and not at all with `headless.logs: off`.
  An answer to a waiting guard approves only as above: a signature the
  swimmer checks against `lookup.yml` over its own statement of the
  question (a hub can't forge, replay or redirect one), or a button where
  `swim.yml` allows it, and never for a guard a `require:` rule names. A
  hub can always withhold answers: the guard then declines.
  The swimmer records every command it takes, including refusals.

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
| A rule change, stricter or looser, stales a standing approval for that guard; a removed key stops counting at once; standing approvals expire, capped by `standing.max`. | `TestApprovalLifecycle` (e2e/custody_test.go), `TestState`, `TestStandingMax` (sigfile) |
| A swimmer's own environment approves nothing; a batch's approvals are recorded as the queue's. | `TestQueueAndWatchOnce` (e2e/queue_test.go) |
| `swim watch -y` is logged as the swimmer's. | `TestWatchYes` (e2e/queue_test.go) |
| What `-y` (and a guard flag) approved shows in `swim status`, the round's chain entry and `swim history`, the CI summary and annotations, and the hub. | `TestAutoApprove` (e2e/approval_test.go), `TestCIGitHub` (e2e/ci_test.go), `TestHubSnapshotUnprompted` (cli), `TestHubFlow` (hub) |
| `swim watch --approved` runs a script once it is fully approved. | `TestWatchSigned` (e2e/custody_test.go) |
| A script edited after approval no longer matches its approval. | `TestSignedApprovalAndVerify` (e2e/custody_test.go) |
| A lane edited after it was queued is skipped. | `TestQueueAndWatchOnce` (e2e/queue_test.go) |
| A lane whose env files changed after it was queued is skipped. | `TestEnvFileQueuePin` (e2e/envfile_test.go) |
| The bytes approved are the bytes that run, even if the file changes mid-run. | `TestManifestStagedBytesRun`, `TestManifestMidRunEdit` (e2e/manifest_test.go) |
| An approval covers sourced files, the sidecar, the runner and policy. | `TestManifestApprovalBinding`, `TestManifestRunnerBinding` (e2e/manifest_test.go) |
| A queued lane whose manifest changed is skipped. | `TestManifestQueuePin` (e2e/manifest_test.go) |
| The broker names the lane that asks; an approval is used once, by its lane. | `TestManifestRequestsFromTheLane`, `TestManifestResponseReuse` (e2e/manifest_test.go) |
| The hub refuses plain HTTP off loopback, and clients pin its CA. | `TestHubTLS`, `TestHubTLSPolicy` (e2e/hub_tls_test.go) |
| Hub commands need a user with the right role; the admin and swimmer tokens send none; a browser session's forms need its CSRF token, and its cookie is `HttpOnly`, `SameSite=Strict` and (under TLS) `Secure`. | `TestCommandRoles`, `TestLoginAndCSRF` (hub) |
| A swimmer takes each hub command once (replayed seqs and ids are ignored, a new hub epoch starts over) and never after its own `command_ttl`. | `TestAcceptCommandsReplay`, `TestCommandDeadline` (cli), `TestHeadlessHubCommands` (e2e/headless_cmds_test.go) |
| A hub command pinned to a run, script or manifest that changed is refused as stale; `run` is refused unless `swim.yml` allows it, and queues an unsigned batch with no approvals; a planted secret never reaches an uploaded log, and `headless.logs: off` refuses logs; every command is recorded in `.swim.log` and the chain. | `TestHeadlessHubCommands` (e2e/headless_cmds_test.go) |
| The hub keeps uploaded logs in memory only, bounded per log, in all and in time. | `TestLogCacheBounds`, `TestLogUpload` (hub) |
| A signed answer through the hub counts only for the exact question: any field changed, or a key `lookup.yml` doesn't list, fails it. | `TestVerifyHubAnswer` (sigfile), `TestHeadlessHubApprovals` (e2e/headless_answers_test.go) |
| Signed answers through the hub count toward `lookup.yml`'s rule signer by signer; a hub button never satisfies a `require:` rule, and counts only under `headless.approvals: hub` from a listed user; the launcher checks the answer again. | `TestHubAskerSigned`, `TestHubAskerButton` (cli), `TestHubAnswerChecked` (launcher), `TestHeadlessHubApprovals` (e2e/headless_answers_test.go) |
| No answer through the hub by `headless.answer_timeout`, the lane giving up, or the hub gone is a decline. | `TestHubAskerNoAnswer` (cli), `TestHeadlessHubApprovals` (e2e/headless_answers_test.go) |
| An answer names a question the swimmer waits on; a viewer may send only signed answers. | `TestAnswerCommands` (hub) |
| Under `swim run`, a lane can't approve itself: approvals or `lookup.yml` written after the run starts don't count, `swim _mark APPROVED` is refused, and an entry written by a lane that hid from swim run fails `swim verify`. | `TestHostileLaneWritesApprovals`, `TestHostileLaneMarksApproved` (e2e/authority_test.go) |
| `FLAG=1` set inside a lane doesn't approve; set in swim run's environment, it does, and the run's attestation lists it. | `TestHostileLaneSetsFlag` (e2e/authority_test.go) |
| The chain names the lane and job swim run knows, whatever the lane claims; its description is labelled as the lane's. | `TestHostileLaneWrongJob` (e2e/authority_test.go), `TestServerUsesRegisteredLane` (approval) |
| A lane can't mark, stage or finish another lane's round, even with that lane's token. | `TestLaneCantReportForAnother` (e2e/authority_test.go), `TestPeerOutsideTheLane` (approval) |
| The approval socket declines oversized, malformed and slow requests, made-up tokens and lanes that hang up. | `TestBrokerHostileClients` (e2e/authority_test.go), `TestHostileRequests`, `TestDisconnectCancels` (approval) |
| A guarded secret is refused to a lane whose guard wasn't approved (skipped, set by the lane itself, a dry run), and released only to the approved lane process, on the prompt, `-y`, a signed approval, a queued batch's `--approve` or swim run's own `FLAG=1`; each release and refusal is in the chain and `.swim.log`, and `release: step` hands it out once. | `TestGuardedSecretRefused`, `TestGuardedSecretReleased`, `TestGuardedSecretSignedAndQueued`, `TestGuardedSecretReleaseStep` (e2e/guarded_secrets_test.go), `TestBrokerGuardedSecrets` (secrets) |
| The secrets broker believes no lane a request names: it asks the launcher, from the lane process's token and the process that sent it, and gives a lane only the masks of secrets it may get. | `TestBrokerGuardedSecrets` (secrets), `TestNoSecretLeaks` (e2e/leak_test.go) |
| A planted secret (a vault secret, a multi-line one, a `secret_env` variable, a CI stand-in, a guarded one), printed raw, base64- or URL-encoded, split across writes, in labels, guard descriptions, messages, notes, outputs and errors, is in no file swim writes, no output it prints, no hub snapshot and no process's command line; a file no known writer accounts for fails the test. | `TestNoSecretLeaks` (e2e/leak_test.go) |
| Changing, removing or reordering a chain entry is detected. | `TestChainLinksAndDetectsTampering` (chain) |
| An edited log or chain entry fails `swim verify`. | `TestSignedApprovalAndVerify` (e2e/custody_test.go) |
| Every approval is recorded with its method and signed by the runner; the scripts that ran are kept; `signing: required` refuses without a key; `--strict` fails on unsigned entries. | `TestChainRecordsEveryApproval` (e2e/custody_test.go) |
| Entries signed with an old runner key still verify. | `TestRecoveryLostRunnerKey` (e2e/recovery_test.go) |
| `git push`, `commit` and `pull` are blocked in the script, at the step and by the shim. | `TestBlockedCommands` (e2e/e2e_test.go) |
| A chain re-signed wholesale with a new key is consistent but not trusted; with an anchor, not anchored either. | `TestTrustCatchesWholesaleReplacement` (chain), `TestSecurity` (e2e/security_test.go) |
| A `trust.yml` edit counts only when signed by a pinned key; trust is pinned on first use, or by hash in CI. | `TestTrustPinTransitions` (trust), `TestVerifyTrustedLevel` (e2e/verifylevels_test.go) |
| A chain truncated below a checkpoint, or replaced, fails `anchored`; a tampered checkpoint fails its signature. | `TestCheckpointCatchesTruncationAndReplacement` (chain), `TestVerifyAnchoredLevel` (e2e/verifylevels_test.go) |
| Anchors only go forward: a file anchor refuses a diverged export, the hub a seq that goes back. | `TestFileAnchorAppendsOnly` (anchor), `TestHubCheckpointsGoForward` (hub) |
| An approval that didn't meet its `lookup.yml` rule fails `authorized`. | `TestVerifyAuthorizedLevel` (e2e/verifylevels_test.go) |
| `status.yml`, queue entries, approvals files and kept scripts are replaced atomically and synced to disk with their directory; chain appends are synced. | `TestStateWritesAreDurable`, `TestWriteAtomic` (fsutil) |
| Of several processes claiming one lane at once, exactly one wins; a pid reused by another process doesn't hold a lane. | `TestClaimRace`, `TestRunningChecksStartTime` (lane) |
| A lane killed mid-step is never reported passed nor rerun on its own: the next `swim run` or `swim watch` records the round as interrupted, in the log and the chain. | `TestReapDeadLane` (e2e/recovery_test.go), `TestReapDead` (launcher) |
| A queue claim is orphaned once its swimmer is gone: by pid and start time on its host, by an expired heartbeat lease from another. | `TestWorkerLease` (queue) |
| A crash at any chosen point (mid-step, mid-claim, mid-write, mid-append) leaves state that a restart reads correctly: no false success, no silent re-run, a whole `status.yml`, a repairable chain. | `TestFaultStepAfterExit`, `TestFaultQueueClaimed`, `TestFaultWatchMidBatch`, `TestFaultStatusMidWrite`, `TestFaultChainMidAppend` (e2e/fault_test.go) |
| Several `swim watch` processes on one queue run each batch once, and lanes sharing a lock never overlap. | `TestFaultManyWatchers` (e2e/fault_test.go) |
| The scheduler never starts a lane before its `After:` lanes finish, never lets lock holders overlap, and never reports a failure as a pass, on random graphs. | `TestSchedulerProperties` (e2e/property_test.go) |
| A lane's state moves only as the state machine allows; a finished round's result never changes in place. | `TestTransition`, `TestUpdateChecksTransitions` (status) |
