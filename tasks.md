# Tasks

Work arising from two external reviews (2026-10-14): a design review and a
first-pass security audit. The design review read the
README, the brief, go.mod and the CI lane. The audit read `internal/chain`,
`internal/sign` and `internal/approval`. Each task
below has been checked against the code. **Today** says what exists, with
file:line references. **Do** says what's missing. **Done when** gives the
acceptance check.

Priorities:

- **P0**: the claim the product makes isn't backed by the code yet
- **P1**: needed before teams rely on it
- **P2**: adoption

Kinds (the audit's advice: keep these apart in issues and PRs):

- **bug**: a defect reproduced or confirmed in the code
- **gap**: a missing guarantee or design decision
- **hypothesis**: an attack scenario nobody has tested yet

The [classification table](#classification) gives each security task's kind.

Larger designs go in `features/`, not here.

The review's main point is the one to keep in mind throughout. Approval,
signing and the chain prove **who** decided **what**. They don't make an
arbitrary script safe. Every task should make that line sharper, not blur it.

---

## Phase 1: prove the safety guarantees

### T1. Run the bytes that were approved (P0, security)

Spec: [features/execution-manifest.md](features/execution-manifest.md).

**Today:**
- The runner execs the script's path in the working tree (`runner.go:132`,
  `launcher.go:950`).
- bash reads the script incrementally, so an edit made mid-run changes what
  runs.
- `keepScript` copies the script to `.swim/scripts/<sha>.sh`
  (`custody.go:52`), but only for audit.
- For queued runs, the SHA is re-checked (`launcher.go:620`) after locks are
  taken but before waiting for a slot. That leaves a window.

**Do:**
- Hash once, copy into `.swim/scripts/<sha>`, and exec **the copy**.
- Do the same for group files (`lanes.<group>.sh`) and for `Includes:` files,
  staging them into a per-run directory.
- Re-check the pin right before exec, not before the slot wait.

**Done when:** an e2e test that rewrites `lane.N.sh` and an included file
while the lane waits for a slot, and again mid-run, shows the original bytes
running and the chain naming the original SHA.

### T2. Widen what an approval covers (P0, security)

Spec: [features/execution-manifest.md](features/execution-manifest.md),
which also covers the audit's Finding 4 (observed facts such as `PATH` and the
cloud account).

**Today:** `lane.ScriptBytes` (`internal/lane/matrix.go:350`) covers the
script, `Matrix: @file` values and `Includes:` globs. It doesn't cover:

- files pulled in with `source` that aren't listed in `Includes:`
- the `lane.N.yml` sidecar itself
- the runner chosen by config
- `swim.yml` settings that change behaviour (blocked patterns, secrets,
  runners)

Env files are hashed separately, and only for queued runs.

**Do:**
- Define an **execution manifest**: script SHA, included-file SHAs, sidecar
  SHA, runner, the policy-relevant subset of `swim.yml`, env SHA, job id.
- Approvals sign the manifest hash. `swim lint` fails on a `source` line not
  covered by `Includes:`.
- Document what the manifest deliberately leaves out: external binaries,
  remote state.

**Done when:** a table-driven test shows that changing each manifest input
invalidates a signed approval, and changing anything else does not.

### T3. Stop lanes from approving themselves (P0, security)

**Today:**
- A lane runs as the operator, and so can:
  - write `approvals/`, `lookup.yml`, `.swim/status.yml` (whose `ScriptSHA`
    the guard trusts, `custody.go:74`) and `.swim/chain.jsonl`;
  - call `$SWIM_BIN _mark N APPROVED ...` directly, which writes a "flag"
    approval signed by the runner (`hooks.go:716`);
  - set `FLAG=1` in its own environment before calling `guard`
    (`lib.sh:421`).
- The approval socket has no token, and its `Request.Lane` is whatever the
  caller says (`approval.go:27`). The SDK socket does check a token
  (`lanesock.go:140`).

**Do:**
- Treat the launcher as the only writer of approval state. `_mark APPROVED`
  should only be accepted with a per-run secret that `guard` gets from the
  launcher and the lane body can't read. Short of that, have the launcher
  verify each APPROVED mark against the decisions it actually brokered.
- Read `ScriptSHA` from the launcher's in-memory manifest (T2), not from
  `status.yml`.
- Give the approval socket a per-lane token, as the SDK socket has, and bind
  the socket to its lane.
- Snapshot `approvals/` and `lookup.yml` at round start, and verify against
  the snapshot.
- **The audit's Finding 3:** the approval prompt shows `lane`, `job` and
  `desc` exactly as the caller sent them. Build the question from the
  launcher's own state instead, refuse flags the script doesn't declare,
  label the lane's description as the lane's, and accept each response only
  once. All of this is specified in
  [features/execution-manifest.md](features/execution-manifest.md).

**Done when:** the e2e tests below each fail to produce an approved guard or a
chain entry that `swim verify` accepts:
- a hostile lane that edits `approvals/`
- a hostile lane that calls `_mark APPROVED`
- a hostile lane that sends a request for another lane on the socket
- a lane that sends a misleading description or a job id that doesn't match
- a lane that replays another lane's approval response

Also cover the broker's edge cases (`approval.go`), each of which must end in
a decline:
- the client disconnects mid-question
- the request is cancelled
- the deadline passes
- two lanes ask at the same moment

### T4. Write down the trust model (P0, docs)

**Today:**
- `guard` is a bash function (`internal/assets/lib.sh:402`): advisory by
  design, and a script can simply skip it.
- Failing closed is real: no TTY, a socket error, a deadline or a stop all
  decline (`approve.go:270`, `approval.go:122`).
- None of this is stated as a guarantee anywhere a reader would find it.

**Do:** add a "What swim protects against" section to the README and
`swimlane.md`.

- **In scope:** an agent with no credentials; edits made after approval;
  forged or tampered approvals; tampering with the record.
- **Out of scope:** a malicious script that is approved and run with broad
  credentials. Guards and lint are conventions, not a sandbox. Point to scoped
  credentials and OS isolation for that.

Say plainly that `-y`, `--approved` and `FLAG=1` bypass prompts, and how each
one is recorded.

Use the audit's three invariants as the headings:

- **Authorization:** nothing that needs approval runs without valid
  authorization for that exact operation.
- **Integrity:** what runs matches what was approved.
- **Attribution:** every run traces back to its job, worker, approval and
  outcome.

To move guards from a convention to an enforced boundary for credentials,
see [features/guarded-secrets.md](features/guarded-secrets.md), which
answers the audit's Finding 1.

**Done when:** the section exists and the help guide links to it, and each
in-scope claim names the test that proves it (T1–T3, T6–T8, T25).

### T5. Make `-y` loud everywhere it's used (P1, security)

**Today:** auto approvals are recorded (`launcher.go:381`; chain
`method: "auto"`, `approve.go:197`). They aren't surfaced in CI summaries,
`swim status` YAML or the hub.

**Do:** add `auto_approved: [flags]` to the status and history YAML, a line in
the CI summary, and a badge on the hub worker page.

**Done when:** a `-y` run is visible in all three without reading the chain.

### T6. Close the crash windows (P0, correctness)

**Today:**
- Locks, the worker lock and queue claims are sound:
  - locks and the worker lock use flock, released when the process dies;
  - queue claims are `O_EXCL` plus an atomic rename (`queue.go:143,341`).
- The gaps:
  - **The lane PID claim is check-then-write**, with no `O_EXCL`
    (`lane.go:651`), and trusts `kill(pid,0)`, so a reused PID can fool it.
  - **No `fsync` anywhere.** Temp-then-rename without fsync can leave a file
    empty or old after power loss.
  - **Status stays `running`** until someone runs `swim status`
    (`cli/status.go:272`).
  - **Orphaned queue entries on other hosts** are assumed alive
    (`queue.go:296`).

**Do:**
- Claim lanes with `O_EXCL` or flock, and record PID plus start time (or a
  flock held by the lane) instead of the bare PID.
- fsync the file and its directory on status, queue, chain and approvals
  writes.
- Have the launcher and watch mark dead lanes on startup.
- Give claims from other hosts a lease and heartbeat.

**Done when:** the T7 fault-injection tests pass.

### T7. Fault-injection and multi-process tests (P0, testing)

**Today:** good concurrency coverage already exists:
- status (`status_test.go:62`)
- chain (`chain_test.go:62`)
- queue claim (`queue_test.go:46`, `e2e/queue_test.go:125`)
- locks (`e2e_test.go:1265`)
- interrupts (`e2e/interrupt_test.go`)

There are no property-based or fuzz tests, and no kill-at-a-chosen-point
tests.

**Do:**
- **A failpoint hook** (env `SWIM_FAILPOINT=name`, compiled into tests only)
  at these points:
  - after a step's command exits, before its PASS/FAIL line is written
  - after a queue claim's rename
  - mid-`status.yml` write
  - mid-chain append
- **Kill at each failpoint.** The test kills the process there and checks
  that a restart neither reports success nor silently re-runs the step.
  Retried non-idempotent steps must need `--retry` or an explicit requeue.
- **N real `swim watch` processes on one repo:** a batch runs exactly once,
  and no two holders of one lock overlap (check with timestamps the lanes
  write).
- **`testing/quick` or rapid** on the scheduler: a dependent never starts
  before its `After:` passes, locks are exclusive, and a failure never turns
  into a pass.
- **Fuzz targets** for the script header parser, the `logparse` line parser,
  the chain line decoder and the lane socket framing.

**Done when:** these run in `lanes.ci.sh` and fail when T6's fixes are
reverted.

### T8. Detect a truncated or replaced chain, and export it (P1, security)

Spec: [features/chain-anchors.md](features/chain-anchors.md), which answers
the audit's Finding 2.

**Today:**
- Every entry links to the one before it with a hash, has a seq number and
  can be SSH-signed (`chain.go:116`).
- `swim verify` checks links, signatures, kept scripts and log hashes
  (`custody_cmds.go:663`).
- What it can't catch:
  - `chain.Check` (`chain.go:238`) checks each signature against the
    `Signer` recorded **in that same entry**. A chain replaced wholesale,
    signed with a new key, is internally consistent. `--strict` compares
    signers against `lookup.yml`, but `lookup.yml` sits in the repo, which a
    lane can write.
  - Unsigned entries are allowed.
  - Removing entries from the tail is undetectable.
  - Anyone with the runner key can rewrite the whole chain.
- `.swim/chain.jsonl` isn't committed, which is an open question in the brief.

**Do:**
- **Checkpoints:** write a signed checkpoint (seq, head hash) to somewhere
  the lane can't rewrite. Options: the hub, a committed `chain.head`, or a
  `swim chain export` target (file, S3, syslog).
- **`swim verify --against <checkpoint>`** reports truncation.
- **`--strict` fails on unsigned entries.**
- **Close the open question** in `swimlane.md`.

- **Report three levels in verify:** consistent, trusted (signers pinned
  outside the repo) and anchored.
- **Stop merging outcomes into one success message (bug, confirmed).**
  `cmdVerify` prints the green `verified: every link, signature, log and
  script checks out` even after it has warned about unsigned entries or
  signers missing from `lookup.yml`. Only `--strict` turns those warnings into
  failures. Report each level separately and never print `verified` for one
  that wasn't checked.
- **Add a fourth level, authorized:** the approvals recorded in the chain must
  have met the `lookup.yml` rule in force for that guard, with signers whose
  roles allow it. `internal/chain` itself stays free of policy, and the CLI
  layers this check on top.

**Done when:** tests that truncate, reorder, re-sign or **replace the whole
chain with a new key** are each caught by verify with a pin or a checkpoint
present, and the docs say what verify can't catch without one.

### T9. Prove secrets don't leak (P1, security)

**Today:**
- Masking is value-based (`redact.go`) and covers:
  - step output
  - wrapped stdout/stderr (`wrap.go:100`)
  - the launcher stream (`launcher.go:996`)
  - guard descriptions
- Secrets reach lanes through env (`launcher.go:983`).
- Tests exist (`e2e/secrets_test.go`, `redact_test.go`).

**Do:** a single leak test that plants a secret and checks every place swim
writes:
- logs and archives
- `.swim.log`
- the chain
- status and history YAML
- `outputs/`
- queue results
- hub snapshots (`Tail`, `Events`, `Message`)
- error messages
- `/proc/<pid>/cmdline` of steps

Include encodings: base64, URL-encoded, split across lines.

**Done when:** that test exists and covers every writer. A new writer added
without masking fails it.

### T10. Harden the protocol between child and swim (P1, security)

**Today:**
- The SDK socket is 0600, token-checked, bound to its lane and capped at 8 MB
  per line (`lanesock.go:91-140`).
- The approval socket has none of that beyond its 0700 directory (T3), and no
  size cap.
- Hidden commands (`_mark`, `_finish`, `_stage`) take the lane number as an
  argument.

**Do:**
- Give the approval socket a size cap and a token (folded into T3).
- Make hidden commands require the per-run secret, so a lane can't report for
  another lane or job.
- Write the authority rule in `proto/` docs: the exit status and swim's own
  log lines decide pass or fail; the SDK can only add FAIL.

**Done when:** a lane calling `_mark`/`_finish` for another lane is refused,
with a test proving it.

### T11. Process cleanup on timeout (P1, correctness)

**Today:**
- Lanes get their own process group, and interrupts escalate to the group
  (`launcher.go:991`, `interrupt.go:19`).
- **But step timeouts signal only the step's process, not its group**
  (`step.go:397`), so a `bash -c 'sleep 999 & wait'` step can leave
  grandchildren behind.

**Do:** run steps in their own group, and on timeout send TERM, then KILL, to
the whole group.

**Done when:** a test whose timed-out step forked children shows no survivors.

### T24. Chain append fails after a record over 256 KiB (P1, bug)

Confirmed in the code.

**Today:** `lastLine` (`internal/chain/chain.go:166`) reads only the last
256 KiB of the file to find the previous entry. When the final record is
bigger than that:
- `LastIndexByte` finds no newline, so it returns the window starting
  mid-record;
- `json.Unmarshal` fails;
- every later append fails, so swim can't record anything until someone
  repairs the file by hand.

Entries hold output and input hashes, so they grow with large matrix rounds.

**Do:**
- Scan backwards in growing windows until a newline is found, or until
  reaching the start of the file.
- Cap the size of an entry, spilling large lists into a hashed side file
  that the entry references.
- fsync each append for security events (approvals, round start and end,
  secret release) before reporting success. The chain's share of T6.
- Document what to do about an incomplete final record. Today it's refused
  with "swim verify shows where" (`chain.go:182`); add `swim chain repair`,
  which truncates the incomplete tail and records the truncation in a new
  entry.

**Done when:** a regression test appends a 300 KiB entry, appends another
after it, and then `swim verify` passes. A second test kills the process in
the middle of an append (T7 failpoint) and shows the repair path.

### T25. Security regression suite (P0, testing)

The audit's checklist, as one e2e file (`internal/e2e/security_test.go`)
that runs in `lanes.ci.sh`. Each case asserts today's behaviour. When a case
documents a known gap, it's marked `t.Skip("gap: T<n>")`, so turning it on is
part of finishing that task.

| Case | Expectation | Closes with |
|---|---|---|
| A destructive command run without calling `guard` | Recorded as outside the cooperative model (no approval in the chain); a guarded secret is refused | T4, guarded-secrets |
| The whole chain and its signer key replaced | verify: consistent yes, trusted no, anchored no | T8 |
| A guard request with a misleading description or the wrong job id | Prompt shows launcher-derived lane and job; chain attributes to the true lane | T3 |
| An approval response replayed from another lane | Refused | T3, T10 |
| An included module, the runner, the env or the evidence changed between approval and exec | Lane refused or approval stale | T1, T2 |
| A chain record over 256 KiB, then another append | Both appended; verify passes | T24 |
| A worker killed mid-step and mid-chain-append | No false success; no silent re-run; chain repairable | T6, T7 |
| A planted secret in every output | Not found in any file swim writes | T9 |

**Execution-boundary fixture (the audit's Patch C).** A temp repo with a
harmless stand-in for a destructive command (it touches a marker file),
behind `guard`. These cases cover the path from `guard` to process launch:

1. Without approval, the marker isn't created.
2. A declined or timed-out approval doesn't create it.
3. An approval for another script or job doesn't authorize it.
4. Changing the script after approval invalidates the approval.
5. A queued job can't run a different script than the one queued.
6. The final chain entry names the script SHA, approval and outcome that
   actually ran.

**Done when:** the file exists with every case, and the skips are removed as
each task lands.

### T26. Sanitize untrusted text in prompts and screens (P1, security)

**Today:**
- `approval.Question` (`approval.go:214`) puts the lane's `Desc` and
  `Evidence` into the prompt as-is.
- `askTerminal` (`cli/approve.go:279`) writes that to stderr raw. A lane can
  embed escape sequences, `\r` or backspaces to hide or rewrite what the
  operator approves.
- The live view's renderer (`display/render.go:394`) handles ESC sequences.
  It keeps SGR color codes and strips the rest. Whether other control
  characters (`\r`, `\b`, C1) get through is untested.
- `step.StripWriter` and `lane/msg.go` strip ANSI from logs and messages.

**Do:**
- Add one `ui.Safe(string)` helper: drop C0 and C1 controls except `\t`, all
  ESC sequences, and bidi overrides. Apply it to every place that shows text
  a lane supplied:
  - approval questions
  - `swim msg`
  - step labels
  - hub pages and snapshots
  - YAML output
- The hub's HTML pages already go through `html/template`, but this needs
  checking.

**Done when:** a lane whose guard description contains `\x1b[2K\r`, `\b` and
U+202E produces a prompt that shows them escaped or removed, both at the
terminal and in the live view.

### T27. CI security gates (P1, testing)

**Today:**
- `lanes.ci.sh` runs fmt, build, vet, swim lint, unit tests and e2e.
- It does not run `-race`.

**Do:**
- Add `go test -race ./...` as an Always lane.
- Run T7's fault-injection tests and T25's suite as their own lanes, so a
  failure names the gate.
- Security tests fail the build on a violation. They never only warn or
  report.

**Done when:** `lanes.ci.sh` has a race lane, a fault-injection lane and a
security lane, and reverting a T3 or T24 fix turns CI red.

### Release readiness

Before swim is described as suitable for sensitive production operations,
each of these needs evidence: a passing test or a documented decision.

The supported threat model comes first (T4). A local developer tool and a
multi-user production executor need different answers, so not every
deployment needs every item.

| Criterion | Evidence |
|---|---|
| Approval can't be bypassed through supported execution paths when policy requires it | T3, T25 (fixture cases 1–3), guarded-secrets for credentials |
| Approval is bound to the exact executable inputs and the relevant context | T1, T2, execution-manifest |
| Signer identity is checked against a trust source independent of the chain | T8, chain-anchors |
| Audit durability and recovery are documented and tested | T6, T7, T24 |
| Concurrent workers can't hold conflicting exclusive resources | T6, T7 |
| Logs and approval prompts handle untrusted content safely | T9, T26 |
| Docs separate safeguards from enforceable boundaries | T4 |

### Classification

| Task | Kind | Status |
|---|---|---|
| T1 run what was approved | gap | confirmed in code |
| T2 approval coverage | gap | confirmed in code |
| T3 lanes approving themselves | gap | confirmed in code (`_mark APPROVED`, editable `approvals/`, unauthenticated socket); no exploit written |
| T6 crash windows | bug | confirmed in code (PID claim, no fsync) |
| T8 verify merging outcomes | bug | confirmed in code |
| T8 chain replacement and truncation | gap | confirmed in code |
| T10 hidden commands taking any lane | gap | confirmed in code |
| T11 timeout leaves children | bug | confirmed in code |
| T16 no TLS on the hub | gap | confirmed in code |
| T24 chain append over 256 KiB | bug | confirmed in code |
| T26 escape codes in the terminal prompt | bug | confirmed in code (raw write) |
| T26 control characters in the live view | hypothesis | untested |
| T9 secret leaks | hypothesis | untested |
| Concurrent approval requests and disconnects (T3) | hypothesis | untested |

---

## Phase 2: make it operationally dependable

### T12. Write down state transitions (P1, design)

**Do:**
- Write the lane, job, queue-entry and approval state machines in one place.
- Add a `Transition(from, to)` check that every writer goes through, which
  panics in tests on an illegal move. T7's property tests drive it.

**Done when:** each status, queue or runrec write goes through the check.

### T13. Separate the parts that hold guarantees (P2, maintainability)

**Do:**
- Pull the scheduling and approval decisions in `internal/launcher` and
  `internal/cli` into pure functions that take state in and return decisions,
  with no filesystem or exec.
- Start where T7's property tests need it: `plan.go` and `deps.go` in the
  launcher, and the approval decision in `cli/approve.go`.

**Done when:** those tests run without a temp dir.

### T14. Approval lifecycle (P1, product)

**Today:** approvals can be revoked (sigfile events) and can carry
`--expires`.

**Do:** decide and document:
- whether an approval becomes invalid when the policy changes (`lookup.yml`
  rule, guard set, the manifest in T2), even if the script didn't;
- whether standing approvals (`--auto USER`) must expire.

**Done when:** `swimlane.md` records the decision, with tests.

### T15. Cross-session claims (P1, product)

This is an open question in the brief.

**Do:** decide whether a session claims a lane before writing it, using a
lease with an owner and an expiry and recovery when the session disappears,
beyond the `Owner:` header. If yes, write `features/lane-claims.md`.

### T16. Hub identity and TLS (P1, security)

**Today:**
- The hub uses a shared enroll token plus per-worker and admin tokens.
- It serves plain HTTP (`cli/hub.go:113`).

**Do:**
- Add `--tls-cert`/`--tls-key`, or document that the hub must sit behind a
  TLS proxy, and refuse non-loopback HTTP without `--insecure`.
- Per-user identity is designed in [features/headless.md](features/headless.md).
  Keep the hub read-only until that ships.

### T17. Compatibility and release policy (P1, docs)

**Do:** document:
- supported OSes: Linux and macOS. CI runs on ubuntu-latest and macos-latest;
  there's no Windows support (flock, Setpgid).
- the minimum Go version for source builds (go.mod: 1.27.1)
- per-runtime prerequisites (bash 3.2+, python, node, go)
- what a breaking-version bump (`.swim.lock`) means
- stability tiers for the CLI, the YAML schemas, `swim.lane.v1`, and the hub
  API

---

## Phase 3: reduce adoption friction

### T18. Add a LICENSE (P2)

There's no LICENSE file. Pick one, then add it to the README and the GitHub
About section.

### T19. Five-minute quickstart (P2, docs)

Put a block at the top of the README that needs no cloud credentials:
`swim init`, `swim new` a harmless lane, `swim plan`, `swim run`,
`swim log`, `swim verify`. Move the feature inventory below it.

### T20. Architecture diagram (P2, docs)

Draw planner → lane script → approval (signed) → operator/worker → chain/log.
Mark where credentials live and where policy is enforced, and keep it
consistent with T4.

### T21. Repo landing page (P2)

Add to the GitHub repo:
- a description and topics
- a CI badge (`.github/workflows/test.yml`) and a license badge
- one terminal recording of `swim run` in the live view

### T22. Recovery playbook (P2, docs)

Write a "something went wrong" page:
- an orphaned queue entry (`swim queue requeue`)
- an interrupted round
- a lane stuck `running`
- `swim verify` failing
- a lost runner key

### T23. Outside feedback (P2)

Once T19 lands, have two engineers who didn't build swim run a real change
with it. Record the friction here.

---

## Features staged from these reviews

- [features/headless.md](features/headless.md): headless workers driven from
  the hub (approvals, interrupts, manual runs, per-job logs). Depends on T3,
  T10 and T16, because it puts approvals on a network path.
- [features/execution-manifest.md](features/execution-manifest.md): stage and
  pin everything an approval covers, and generate approval questions from the
  launcher's own state. Covers T1, T2, and part of T3 and T14.
- [features/guarded-secrets.md](features/guarded-secrets.md): a vault secret
  is released only after its guard is approved in this run. This turns guards
  into enforcement for credentials (audit Finding 1).
- [features/chain-anchors.md](features/chain-anchors.md): pinned signers,
  external checkpoints, and verify levels for consistent, trusted and anchored
  (audit Finding 2). Covers T8.
