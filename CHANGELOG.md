# Changelog

What changed in swim, newest first. Each `##` section is one revision.
`swim changelog` prints this from the binary you are running:

```sh
swim changelog                  # the newest revision (what you just updated to)
swim changelog -n 3             # the newest three revisions
swim changelog --since 0.20261014.1   # everything after the version you came from
swim changelog --all            # every revision
```

Versions are `0.<YYYYMMDD>.<index>` (`swim --version`): the release's date and
its number that day, tagged `v0.<YYYYMMDD>.<index>`. Until 0.20261014.1 they
were `<breaking>.<YYYYMMDD>`, tagged `v0.<breaking>.<YYYYMMDD>`. The version
says nothing about compatibility: lane scripts, logs and swim's state have
syntax versions, which a repo's `.swim.lock` pins. A revision that changes
one has **Upgrading** notes; read them and `swim lock --help` before running
lanes. Revisions before versioning existed are named by commit.

To cut a release:
1. `make next-tag DRY=1` prints the next tag, `v0.<today>.<index>`.
2. Rename `## Unreleased` to `## 0.<date>.<index> (v0.<date>.<index>, <date>)`,
   and start a new empty `## Unreleased` above it.
3. Commit, then run `make release V=v0.<date>.<index>`. It refuses to tag
   unless the date, the index, the changelog and a clean tree all agree.

Or tag what's on main as it is: `make next-tag` tags HEAD (a clean tree,
already pushed to origin's main) with the next tag and pushes that tag.

## Unreleased

### Upgrading

- **Script syntax 4 (modules):** a lane that uses `# Modules:`, `mod` or
  `mod_env` declares `# swim: syntax 4` (lint errors without it), so a swim
  without modules refuses it instead of running it with every module call
  failing. Repos locked at script 3 refuse to run until upgraded: once
  everyone who runs lanes in the repo has this swim, run `swim lock
  --upgrade` and commit `.swim.lock`. Existing syntax 2 and 3 scripts run
  as they are.
- **Approvals bound to a manifest go stale once.** swim.yml's `modules:`
  joined the policy hash (policy version 3), so every manifest's hash
  changes with this swim, whatever the repo's swim.yml says. Approve the
  lanes you need again once everyone runs this swim; queued batches pinned
  to a manifest are skipped as changed, so queue them again.

- **State syntax 2 (execution manifests):** repos locked at state 1 (or
  `breaking: 7`) refuse to run until upgraded. Chain entries, signed
  approvals and queued batches now name the manifest they ran or approved
  (`manifest_sha256`), which a swim of state 1 can't verify. Once everyone
  who runs or approves lanes in the repo has this swim: let running lanes
  finish, run `swim lock --upgrade`, and commit `.swim.lock`. CI reads the
  committed lock, so it needs that commit before it runs.
  - **Approvals:** existing `approvals/*.signature` files still count, for
    the script hash alone. Approve again to bind the manifest.
  - **Queued batches** from an older swim still run, pinned to their script
    and env hashes as before.
  - **Lanes now run a staged copy.** `$0` is the copy under
    `.swim/runs/<run>/<N>/`, and the working directory is still the repo
    root. A lane that finds files next to itself with `dirname "$0"` should
    use the repo root instead. A Python or TypeScript lane that imports a
    local module by a path swim can't see should list it in `# Includes:`.
    A `declare` at the top of a file a bash lane sources is now local to
    swim's `source` wrapper; use plain assignments.
  - **Undeclared guard flags are refused** under `swim run`: a flag named
    only at run time (`guard "$FLAG"`) must be listed in `# Guards:`.
  - **Standing approvals now expire and bind `lookup.yml`'s rules.** An
    existing `approvals/auto.USER.signature` (never expiring, no rules
    bound) still counts, with a warning in `swim approve`, `swim doctor`
    and `swim verify`, until `lookup.yml` sets `standing: {max: ...}`; then
    it stops counting. Revoke it and approve again to renew it on the new
    terms. An older swim can't check a new standing approval's signatures.
- **Hub state v2:** `swim hub --state DIR` reads a `swim.hub-state/v1`
  file and saves it as `swim.hub-state/v2` (users and commands added) when
  it starts; an older swim hub refuses the new file. Workers, approvals and
  grants carry over.
- **Manifest policy version 2 (guarded secrets):** `swim.yml`'s policy part
  now covers `secrets:` and `ambient_credentials:`, so every lane's
  execution manifest hash changes once. Signed approvals bound to a
  manifest stop counting (approve again: `swim approve N`), and a queued
  batch pinned to a manifest is skipped (queue it again). Approvals of the
  script alone, from before manifests, are unaffected.
- **SDKs and the secrets broker:** the broker no longer believes the lane a
  request names; it asks swim run, from the lane process's own token
  (`SWIM_APPROVAL_TOKEN`, sent as `lane_token`). The SDKs in this release
  send it. An older SDK still gets unguarded secrets; a guarded one is
  refused to it.
- **`swim verify` reports levels:** the green `verified: every link,
  signature, log and script checks out` line is gone. Each level gets a line
  instead (`consistent  yes  ...`). CI that greps the output should use
  `swim verify --yaml` and read the level fields.

### Docs

- **New pages under `docs/`:** the [trust model](docs/trust-model.md) (what
  swim protects against, what it doesn't, and the test behind each claim),
  the [architecture](docs/architecture.md) (where credentials live and where
  policy is enforced), the [state machines](docs/state.md), a
  [recovery playbook](docs/recovery.md) and the
  [compatibility policy](docs/compatibility.md) (platforms, prerequisites,
  stability tiers, deprecations).
- **README:** a quickstart that needs no credentials (an e2e test runs it as
  written), "What swim protects against", the architecture diagram, a CI
  badge, and current version strings.
- **`swim --help`** points to the trust model and the recovery playbook.

### Security

- **A lane can't approve its own guard by changing its environment
  (T28):** a lane that unset `SWIM_APPROVAL` or `SWIM_LAUNCHED` and set the
  guard's flag got its guard approved, and when swim run couldn't start its
  approvals (a `TMPDIR` too long for a unix socket) the forged approval even
  passed `swim verify`. Whether a lane runs under swim run is now told by
  who started it, not its environment: swim's hidden commands refuse an
  `APPROVED` mark there without the launcher's approval, and `_approve`
  fails closed (a dry run, with a `WARN` saying why). A run whose approvals
  couldn't start says so in its attestation (`authority_unavailable`), and
  `swim verify` breaks on an approval in a run swim run started but never
  attested. Running a lane directly with `FLAG=1 bash lane.N.sh` approves
  as before.
- **Guarded secrets:** a secret can be guarded by a guard flag in the
  committed `swim.yml`; the run's broker hands it to a lane only once that
  lane's guard was approved in this run, so a script that skips `guard`
  never gets the credential.
  - `secrets: {NAME: {guard: FLAG | [FLAGS], release: lane | step}}`, in
    `swim.yml` only (an error in `config.yml`), plus `ambient_credentials:`
    (AWS profiles with the same access).
  - Counts: the prompt and the live view's pre-answer, `-y`, signed
    approvals, a queued batch's `--approve`, and `FLAG=1` in swim run's own
    environment, as the launcher recorded them for that lane process. Not
    `FLAG=1` a lane sets itself, a `.lane.N.rc`, a declined or dry-run
    guard, another lane's approval, or `swim plan --check`.
  - A refusal (`refused: NAME is guarded by FLAG, which isn't approved in
    this run`) is a `WARN` in the lane's log, a `secret-refused` line in
    `.swim.log` and a `secret-refused` chain entry; a release is a
    `secret-released` entry naming the flag, how it was approved and the
    approve entry (never the value), which `swim verify` checks against the
    run's attestation. `swim history` shows both.
  - `release: step` hands the value out once. `SWIM_SECRET_<NAME>` of a
    guarded secret never reaches a lane's environment. `swim secret grant
    --guard FLAG` guards a hub grant (both guards must be approved);
    `swim secret grants` and `swim secret ls` show guards. Outside a run,
    `swim secret get` of a guarded secret works at your shell (recorded in
    `.swim.log`) and is refused in a lane run with bash.
  - `swim lint`: `secret-guard` (a guarded secret got with no guard of it
    first), `secret-argv` (`$(swim secret get ...)` inline in a command's
    arguments, readable with `ps`), `ambient-credential` (a listed profile
    used directly). `swim doctor`: `guarded-secrets` (lists them; warns when
    the secrets key has no passphrase, which lets a lane decrypt the store
    itself; `swim run` warns too) and `ambient-credential` (a listed
    profile configured in `~/.aws`).
- **The secrets broker gave every value to any lane (fixed):** its `masks`
  request returned every secret's value to anything holding the run's
  token, and `get` recorded whatever lane the request claimed. Now the
  broker identifies the lane process from its approval token, checked
  against the process that connects, and a lane's masks are the secrets it
  may get: the unguarded ones and the guarded ones released to it. swim
  run's own stream still masks them all. A masker that lives long (a
  wrapped lane's log, a step that runs a while) picks up a secret released
  after it started.
- **More masking:** an SDK lane's in-process step label (status.yml's
  current step, the hub) and `swim output KEY VALUE` values are masked.
  (Files a lane writes to `$SWIM_OUT` itself are still its own.)
- **Proof that secrets don't leak (T9):** `TestNoSecretLeaks` plants
  secrets of every kind, prints them every way a lane can (raw,
  base64, URL-encoded, split, in labels, guard descriptions, messages,
  notes, outputs, handlers, errors), drives every writer (plain and YAML
  streams, status, history, logs and archive, queue and watch, the hub),
  and fails on a value in any file, output, hub page or process command
  line, or on a file no known writer accounts for.
- **`swim verify` says how far the chain can be believed,** one level per
  line, each saying what it rests on (T8):
  - **consistent:** links, signatures against the key recorded in each
    entry, logs, kept scripts, staged copies and signed approvals: what
    verify checked before. A chain replaced wholesale and re-signed with a
    new key still passes this.
  - **trusted:** every signature is by a key in `trust.yml` (new, committed),
    whose hash is pinned outside the repo in `~/.config/swim/trust-pins.yml`
    on first use or by `swim keys pin`. A change to `trust.yml` counts only
    when signed by a key it already trusts (`swim keys trust KEY --sign`,
    recorded in `trust.yml.sig`); a hand edit fails the level. CI pins by
    hash: `--trust-pin SHA256` or `SWIM_TRUST_PIN`.
  - **anchored:** the chain still holds the entry the newest checkpoint
    recorded. Checkpoints (seq, head hash, time, host, signed by the runner)
    go to each `chain_anchors` destination at the end of every `swim run`,
    `swim all`, `swim ci` and `swim watch` batch, and on `swim chain
    checkpoint`. Destinations: `file:/path` outside the repo, and `hub`
    (new `PUT/GET /api/workers/{id}/checkpoints`; the hub refuses a seq
    that doesn't go forward). Entries removed from the end, or a chain
    replaced wholesale, after a checkpoint left the machine fail the level.
  - **authorized:** every approval recorded met `lookup.yml`'s rule for
    its guard. Approve entries now record the `lookup.yml` they were made
    under (`lookup_sha256`); a failure under that same file is an error,
    under a `lookup.yml` that changed since a warning. A rule with the new
    `signed: true` fails prompt, `FLAG=1`, `-y` and queue approvals of its
    guards (they still run; verify reports them).
  - **`swim chain export --to file:/path`** copies the entries the
    destination doesn't hold yet (refusing if the chain no longer starts
    with them), and `swim verify --against DEST` checks against it, so a
    lost or wiped repo can still be checked.
  - Exit 1 when a level says no; `--strict` also on n/a (no `trust.yml`, no
    anchors) and partial (unsigned entries). `--yaml` gives each level as a field
    (schema `swim.verify/v1`).
  - `swim lint` fails when `lookup.yml` names a key `trust.yml` doesn't
    list; `swim doctor` reports a `trust.yml` that isn't pinned, or changed
    unsigned (`trust-unpinned`), a chain with no anchors (`anchors-none`)
    and an anchor that missed the latest checkpoint (`checkpoint-unsent`). `swim keys` shows `trust.yml` and its pin.
- **A lane can't approve itself (T3):** under `swim run` the launcher
  decides every guard and writes every approval to the chain; a lane only
  asks.
  - **Behaviour change: `FLAG=1` must come from swim run's environment.**
    The shell that runs `swim run` (or a script that runs it, or a queued
    batch's `--approve`) still approves. A `FLAG=1` a lane sets for itself
    (`export FLAG=1` in the script, or anything it runs) is now a dry run:
    `SKIP ... (dry run: FLAG=1 was set inside the lane, not in the
    environment swim run started with; set FLAG=1 to approve)`. Lanes run
    directly with bash are unchanged.
  - **Signed approvals are read once, as the run starts:** `approvals/` and
    `lookup.yml` written later (by a lane, or by you mid-run) count from the
    next run.
  - **`swim _mark N APPROVED` is refused under `swim run`** without the
    launcher's one-time approval id: `a lane can't approve itself`, exit 2.
    `confirm` answered at the lane's own terminal is still logged, but
    under `swim run` isn't an approval in the chain.
  - **Run attestation:** once its last lane exits, `swim run` appends a
    `run` entry to the chain (each lane's manifest and script, the approve
    entries it recorded, the flags its environment approved). `swim verify`
    fails on an approve entry its run didn't record (or two attestations
    claim) and a round that ran something else; runs without one are noted.
    Runs that share an id (`--run-id`, e.g. a CI pipeline's) each attest
    their own lanes. `swim history` shows it.
  - Lanes still run as you and can read the runner key, so a lane that
    hides from `swim run` can sign an entry: `swim verify` reports it. See
    `docs/trust-model.md`.
- **`-y` is loud everywhere (T5):** guards approved with no one asked are
  shown wherever a run's result is, not only in the log and the chain.
  - **`swim status`** lists them under the lane, in yellow for `-y`:
    `approved without a prompt: DEPLOY (swim run -y)`. `status.yml` (and
    `swim status --yaml`) gains `auto_approved`, `auto_via` (`swim run -y`,
    `queue q-... -y`, `swim watch -y`) and `flag_approved` (`FLAG=1`, a
    batch's `--approve`), cleared when the next round starts.
  - **The round's chain entry** carries `auto_approved`, the guards among
    its approvals that `-y` approved; `swim history` shows it as `Auto:` and
    `swim history --yaml` as `auto_approved`.
  - **`swim ci`** adds `**Approved without a prompt:**` to the job summary
    and a warning annotation for each lane with such a guard.
  - **The hub** shows a `-y` badge on a lane in the dashboard, what
    approved which guard on the worker page and in `swim hub tui`, and
    `swim watch -y` on a worker that runs with it.
- **A lane reports only for itself (T10):** swim's hidden commands
  (`_start`, `_mark`, `_stage`, `_finish`, `_handler`, `_approve`) ask
  `swim run` which lane, cell and run they belong to and refuse another's:
  `lane 1 can't report for lane 2`, exit 2.
  - Lane tokens are bound to the lane's process tree (Linux, macOS): a token
    read from another lane's environment is refused, and so is a lane
    process that daemonized out of its tree. The SDK socket checks the same.
  - The approval socket is `0600`, takes one request of at most 1 MiB,
    sent within 10 seconds, and declines anything else.
  - The protocols say who decides what: `proto/swim/lane/v1` (Authority)
    and the new `proto/swim/approval/v1`.
- **Text a lane supplies is made safe before it's shown:** a guard's
  description and evidence, step labels, command lines, `swim msg`, the
  live view and the hub can no longer carry escape sequences, `\r`,
  backspaces, other control characters or bidi overrides, so a lane can't
  hide or rewrite what the operator approves.
  - Removed: every escape sequence (cursor moves, erases, OSC 8 links,
    titles), C0 and C1 controls except tab, and bidi controls; invalid
    UTF-8 shows as U+FFFD. Tabs, colours in lane output in the live view,
    and UTF-8 text are kept.
  - Step labels and marks are cleaned where they're made, so the log,
    status.yml and `--yaml` output are clean too. The chain keeps a guard's
    description as given (evidence); `swim history` cleans it as it shows it.
  - The hub cleans each worker's report as it arrives (a worker isn't
    trusted either); its pages also escape HTML, as before.

### Changed

- **`make next-tag` tags and pushes.** It tags HEAD with the day's next
  release tag and pushes only that tag, refusing a tree with changes or a
  commit origin's main doesn't have yet. `make next-tag DRY=1` prints the
  tag as it used to.
- **This repo's CI runs its slow suites side by side.** It's five group
  files instead of one lane of eight jobs in a row: lane 1
  (`lanes.ci.sh`: fmt, lint, unit, race) runs first, then e2e (lane 2),
  fault injection (3), security (4) and fuzzing (6) start together, each
  `After: 1`. A run takes about as long as the slowest suite instead of the
  sum. `make ci` runs lanes 1-4 and 6.
  - **The e2e suite runs its tests in parallel.** Each test drives swim in
    its own scratch repo, so `newRepo` marks the test `t.Parallel()`, and
    the scenarios run alongside the Go tests instead of after them.
    `SWIM_E2E_SERIAL=1` runs them one at a time again; a test that times
    things too tightly for a loaded machine goes in `serialTests`.
  - **`-timeout 25m`** on the e2e step (and 12m on fault and security): go
    test's default 10m panicked the whole package on a slow macOS runner.
  - **Fuzzing runs 5s per target locally,** 20s in CI (`CI=true`);
    `FUZZTIME` overrides both.
- **This repo's CI has security gates (T27):** `lanes.ci.sh` gains a race
  job (`go test -race`, Always), and runs fault injection, the security
  suite and the fuzz targets (20s each) as jobs of their own, so a red run
  names the gate. The race job's first run found a data race in the
  approval broker's tests (the read timeout, now atomic).
- **A red `swim ci` says what failed:** a failed step's annotation carries
  its last output lines, and this repo's e2e lane runs `go test -json`
  through `e2etool gotest-report`, so its tail (and the annotation) names
  each failed Go test with its output, and `e2e.xml` has a JUnit case per
  test. A failed e2e scenario's scratch repo is kept under
  `E2E_KEEP_FAILED` and uploaded with the run's artifact (the old "keep
  scenario repos" step ran every scenario again, after the upload).
- **`swim watch` runs queued batches side by side:** a batch no longer
  waits for swim to be idle, only for its own lanes.
  - **Up to `--batches N` at once** (`watch_batches`, default 4), each in a
    process of its own (`swim _batch`), so one batch's guard flags never
    reach another's. A batch starts once none of its lanes is in another
    batch or another swim run; a later batch whose lanes are free starts
    ahead of one that waits. `After:` and locks across batches work as
    across swim runs. `--batches 1` keeps one at a time; a headless worker
    runs one at a time (hub answers reach it in its own process).
  - **The watch screen stays up while batches run:** the queue shows them
    as they go, newly queued batches appear (and start) at once, and
    Enter on a batch follows its output (`.swim/queue/out/`); `r` starts a
    waiting batch out of turn alongside the others. Enter on a running
    lane follows that lane's log directly (`v` shows its batch's output),
    instead of pointing you to the batch in the queue. `l` shows the logs of
    the highlighted lane (every round in its current log) or batch (its
    lanes' latest rounds), running or not. Under the queue, the screen
    streams the latest lines of every running lane's log, the running step's
    output included, sharing the room between them; `o` hides it.
  - **Ctrl-C** interrupts every batch in hand gracefully, as it did the one.
- **`[DRY]` marks guards that ran as dry runs:** a lane whose round
  dry-ran a guard (declined, not asked, refused) shows `[DRY]` in the live
  view, `swim status` (with the flags), the watch screen and the hub;
  `status.yml` lists them as `dry_run`. A waiting batch shows `[DRY]` when
  a guard of its lanes isn't approved by the batch or the worker's `-y`.
- **The watch's batch output view has a verbose mode:** `v` switches from
  the overview to the lanes' own logs, with the running step's output as
  it comes, and back.
- **Approval lifecycle:** every signed approval is active, expired,
  revoked, stale (with why) or insufficient (with why), and `swim approve`
  and `swim verify` say which.
  - **Standing approvals expire:** `swim approve --auto USER` lasts 30 days
    unless `--expires D` says otherwise (it prints the expiry), and never
    longer than `lookup.yml`'s new `standing: {max: 90d}`. Approving again
    once one stopped counting starts a new one. `--expires` takes days
    (`30d`) too.
  - **Rule changes stale approvals:** a standing approval records
    `lookup.yml`'s `require:` rules; once the rule for a guard changes,
    stricter or looser, it stops counting for that guard. (Script approvals
    already bind every rule through their manifest.) So loosening a rule
    can't turn an approval signed under the stricter one into enough.
  - **Who is who is read live:** a key removed from `lookup.yml`, or a user
    removed from a group, stops counting at once.
  - **`swim doctor`** warns of standing approvals on the old terms
    (`standing-approval`).

- **Versions are `0.<YYYYMMDD>.<index>`,** tagged `v0.<YYYYMMDD>.<index>`:
  any number of releases a day, each with the date. `Breaking` is gone.
  - **Compatibility is syntax versions:** `.swim.lock` pins the script, log
    and (new) state syntax a repo uses, so a release that changes no format
    never touches it. A lock that says `breaking: 7` means today's syntaxes
    and needs nothing (`swim lock --upgrade` rewrites it in the new form).
  - `make next-tag` prints the day's next tag; `make release` checks it.
    The lane protocol's `hello` returns `syntax` instead of `breaking`.
  - `swim --version` no longer warns about mis-tagged releases (a tag can't
    disagree with the code any more).

- **Hub link in swim watch:** an enrolled `swim watch` shows the hub link at
  the top of its screen: `(hub online) URL` in green, or `(offline mode) hub
  URL unreachable since hh:mm:ss; retrying` in yellow. The watch keeps
  running and reporting retries either way. While the screen is up, it no
  longer prints the "hub unreachable" and "reachable again" lines over it.
  Without the screen, those lines now read `offline mode: hub unreachable`
  and `hub online again`.
- **No lane count:** swim shows the lanes in use instead of a configured
  `lanes`. A lane is in use while it has a lane script that isn't a stub, a
  current log (`agentN.log`, or a job's or cell's), or a running process.
  - `swim new 7` just starts lane 7 (any of 1..99); `swim archive N` (with a
    stub or no script) drops a lane from `swim status`, the live view, the
    watch view and the hub. Archive now moves a lane's job and matrix cell
    logs too.
  - `lanes:` (config.yml or swim.yml) is ignored, and `swim doctor` says so
    (`lanes-setting`); `swim config lanes` and `--lanes` are gone. So are
    `swim all`'s offer to raise the lane count and `swim ci`'s "beyond the
    configured lanes" failure, and `swim plan --yaml`'s `beyond_lanes`.
  - `# After: 9` for a lane with no script, log or past run is an error (a
    typo), in `swim run` and `swim lint`.
- **Hub TLS:** `swim hub --tls-cert FILE --tls-key FILE` serves https (TLS
  1.2 or later) and reloads the certificate when its files change.
  - **Plain HTTP stays local:** without TLS, `swim hub` refuses an `--addr`
    on every interface (`0.0.0.0`, `::`, `:PORT`) or another host, exit 2,
    before it binds. `--insecure` serves plain HTTP anyway; behind a TLS
    proxy, keep the hub on 127.0.0.1.
  - **Workers and operators:** `swim hub enroll` and `swim hub login` refuse
    an `http://` URL to another machine unless `--insecure`. `--ca FILE`
    trusts a self-signed certificate or private CA, kept in `.swim/hub.yml`
    (or `hub-admin.yml`; `$SWIM_HUB_CA` overrides it for admin commands).
    An unknown certificate's error says to pass `--ca`.
  - Existing `http://` enrollments keep working; `swim doctor` warns about
    one to another machine (`hub-http`).

- **`.lane.N.rc` is deprecated:**
  - **A guard flag it sets no longer approves.** `guard` logs a `WARN` and
    asks as if the flag were unset.
  - **Each round logs a `WARN`** while the file exists.
  - **Migrating:** `swim doctor --fix` moves an rc of plain assignments into
    `.env.swim [lane N]` and keeps the original under `.swim/migrations/`.
  - **Removal:** the file stops loading in the next breaking version.
- **The `.gitignore` block ignores `.env.swim`.**

- **A `lane.N.sh` without `lane_init` runs:** it is a plain program, so it
  runs inside the wrapper and is logged as one step (or its `swim run`
  calls are logged). Before, lint refused it. A script that loads the
  library but forgets `lane_init` is still an error.
- **The `.gitignore` block ignores `lane.[0-9]*`:** this covers scripts in
  any language and their `lane.N.yml`. `swim doctor` reports the old block
  as out of date, and `swim doctor --fix` (or `swim init`) rewrites it.
- **`swim archive 1 2 3` archives lanes 1, 2 and 3:**
  - **Arguments:** every argument is a lane number or job id. Before, the
    second argument was taken as the archive's name.
  - **Naming:** each archive is named after its job id. Use `--as NAME` to
    name them all.
  - **All or nothing:** if any lane is running, has no log, or its archive
    name is taken, nothing is archived and every problem is listed.
  - **Old form:** `swim archive 1 cutover`, a name after one lane, still
    works.

### Added

- **A command that fails outside any step is logged.** `WARN  outside a
  step: <command> (exit N)`, with "command not found" for exit 127. Before,
  only the terminal saw it (an undefined `mod` on an older swim left no
  trace). It changes nothing in the round; steps, conditions and `&&` /
  `||` lists aren't reported.
- **The claude module shows the CLI's stderr only when the call fails**
  (a wrapper's container start-up output no longer fills a passing step),
  and says how many lines it held back.
- **Guard detection ignores quoted text.** `echo "the guard was declined"`
  no longer made `was` one of the lane's guards in `swim plan`, the
  manifest and approvals.
- **Built-in module `report`.** `mod report report.md` prints a round's
  artifact from `$SWIM_OUT` into the step log, headed by its size and
  SHA-256 (the hash the END block records for an output), masked like any
  step output. A missing file fails the step unless `--optional`; `--max`
  caps what's shown. It only shows the file: nothing depends on what it
  says.
- **The claude module names its CLI in the call** (`-- CMD ARGS...` or
  `--bin`); `SWIM_CLAUDE_BIN` is gone. It passes long flags only
  (`--print`), so a wrapper's own short options can't take them, and runs
  in one empty directory outside any repo.
- **Built-in modules `tf`, `smoke` and `claude`.** `mod --evidence tf plan
  DIR` runs init (lockfile read-only) and plan quietly, saves the plan in
  the round's `$SWIM_OUT/.swim-tf/`, and prints only its `terraform show`,
  so an evidence-bound approval covers the plan itself; `mod tf apply DIR`
  (inside a guard) applies that saved plan and refuses one that's missing
  or changed (`--bin tofu` for OpenTofu). `mod smoke FILE` runs a committed
  YAML list of HTTP and TCP checks once, judged by status and time; the
  file must be pinned (`# Includes:`). `mod claude --prompt-file F --out
  report.md [--input F] [--log]` writes an artifact with the Claude Code
  CLI the call names (`-- CMD ARGS...` for a wrapper, or `--bin PATH`,
  else `claude` on PATH; never a setting), with no
  tools, no MCP and a fixed system prompt, everything sent masked; it never
  decides anything. New: `mod --evidence` (and `evidence` in op `module`),
  and `read_only_for` / `destructive_for` in `module.yml`, which lint uses
  (`tf apply` outside a guard warns).
- **Built-in modules `target`, `backup` and `wait`.** `mod target aws
  --account ID` (or `kube --context C`, `gcp --project P`) stops the round
  unless the tools point where the lane says; it's a gate module (`gate:
  true` in `module.yml`, new), so a failure stops the round even under
  plain `mod`. `mod backup rds|rds-cluster|dynamodb NAME` takes a snapshot
  named after the lane and job, waits until it's usable, and records its id
  as output `backup.<kind>.<name>` (and, under `mod_env`, `BACKUP_ID` for
  `on_failure`); `swim plan --check` skips it. `mod wait http URL|tcp
  HOST:PORT|cmd -- CMD|kube-rollout RES` waits until ready, judged by a
  status or an exit code, never by matching output.
- **Modules: reusable utilities lanes declare and call as steps.** `#
  Modules: aws-session clone@1 notify ./modules/x`, then `mod notify
  "#orders" "done"` or `mod_env aws-session --profile p --role ARN`. A
  module is a bash file of functions or a Go program with a `module.yml`
  (`swim.module/v1`: version, commands, declared effects). Each call is a
  step through `swim step`; `mod_env` exports what the module gives the lane
  and masks every value from then on (a credential name needs
  `credentials: true`; `PATH` and `SWIM_*` never). The manifest pins each
  module (`pinned.modules`: name, version, source, hash; a repo module's
  files are staged as includes `from: module`), so editing one, or a swim
  release that changes a built-in, stales approvals of the lanes that use
  it. Built in: `aws-session` (Go, through the aws CLI), `clone` (a full
  commit id only), `notify` (the webhook in the secret
  `SLACK_WEBHOOK_URL`, fails soft). Repo modules live in `modules/<name>/`;
  swim builds Go ones offline with your Go, once per source.
  `swim.yml` `modules: {allow, dir, override}` says which modules lanes may use
  (built-ins only without it). `swim modules [ls|show|new]`; `swim plan`
  and `swim approve` show each module's effects; lint `module` and
  `module-effects`; doctor `module-go`; SDKs `r.module(...)` /
  `module_env` / `moduleEnv` / `Module` / `ModuleEnv` over op `module` of
  `swim.lane/v1`.
- **Cache snapshots: an installed tree kept between rounds.** `# Cache:
  snap:web/node_modules @web/package-lock.json`, then `cache restore
  web/node_modules || { run "npm ci" ...; cache save web/node_modules; }`.
  Keyed by the tree, its `@` files (pinned like lockfiles), the OS, the
  architecture and `--with` strings; a miss passes and returns 1; a key is
  written once; never saved from an outcome handler; a save from a round
  with failed steps is marked and its restores say so; a tree in place is
  never replaced. Each restore goes in the round's chain entry (`cache:`),
  since a restored tree runs unchecked. `swim cache ls` / `rm snap:TREE`
  see them.
- **swim watch's follow view says how the run ended.** Enter on a running
  job or batch follows its output, as before; when it ends, a bar across
  the top says PASSED (green), FAILED (red) or INTERRUPTED (yellow), with
  the exit code and time, and under it the failed steps or the lanes that
  were skipped and why (five lines, then "… N more"). It replaces the
  title's easy-to-miss "· finished". `l` (logs) shows the same header for
  a lane's or batch's last round.
- **Lane claims: a lease on a lane while a session writes it.** `swim new
  N` takes one (`claim_lease`, default 2h) and `swim lint N` refreshes it;
  while it's live, another session's `swim new`, `stub` and `archive`
  refuse the lane, naming the holder. `swim status` shows it and `swim
  plan` warns about it. Starting the lane releases it: a run warns when
  another session held it, and runs anyway. `swim claim N [--for D]`,
  `--release [--force]` and `--break "<reason>"`; `--force` on new, stub
  or archive takes one over, logged as `claim-broken`. A session is
  `SWIM_SESSION`, else `user@host`. Claims are advisory and local to the
  checkout (`.swim/claims/`).
- **`swim pause N` / `swim unpause N`: hold a lane without ending it.**
  The current step finishes, then the round waits before its next one
  (a `run`, `gate`, `guard`, ..., or an SDK lane's next step), its process,
  approvals, slot and locks kept; `PAUSE` and `RESUME` lines in the log
  (which count toward nothing), `paused` in `swim status` and the live
  view. `swim resume` is the same command as `unpause`. A pause never
  expires, and the round's `Timeout:` doesn't count paused time.
  - A lane waiting in a run is held before it starts, holding no lock or
    slot.
  - `swim interrupt N` or the first Ctrl-C ends a paused round
    interrupted, its handlers run; `swim run N` on a new job asks to end
    it (or `--replace`, without a terminal) and runs the new one.
  - `--all` holds every lane and keeps workers from claiming batches until
    `swim unpause --all`.
  - `p` pauses or lets go on the selected lane in `swim run`'s live view
    (`P`: every lane of the run) and in `swim watch` (whose title shows the
    repo-wide pause); the hub has Pause and Unpause buttons (with `pause`
    and `unpause` in `headless.allow`, pinned to the run shown).
  - `swim plan` notes paused lanes; the chain's round entry records
    `paused_s` (and `replaced_by` for a replaced round), and `swim
    timeline` the paused time; `swim doctor` warns about a lane paused
    past `pause_warn` (default 4h) or a repo-wide pause left on.
  - `swim timeline` draws each pause in the lane's bar (`╌`; amber in
    `--html`), from the run record's `pauses`.
  - An SDK lane's in-process steps honour the round's `Timeout:` (paused
    time not counted), and `hello` reports the deadline as it stands.
- **`swim config chime on` covers queued jobs too.** `swim watch` now
  chimes as each batch it runs finishes, by the same settings as `swim
  run`: every batch with `chime on`, a batch that didn't pass with
  `failure`, twice for a failure, and not below `chime_min_s`. Until now
  only a `swim run` / `swim all` you started chimed.
- **`swim trash N`: move one lane out of the way, recoverably.** Its
  script and sidecar go to `.swim/cleaned/<stamp>-lane<N>/` (where `swim
  clean` sets scripts aside, with their SHA-256s), its log is archived, and
  it leaves `swim status`, `swim watch` and the hub. `swim trash --restore
  STAMP` (or `swim clean --restore`) puts it back; `swim prune` deletes it
  for good later; a bare `swim trash` lists them. Refused for a running
  lane, one in a queued or scheduled batch, a group file's lane, and an
  Always lane without `--force`. Recorded as `trash` and `untrash` in
  `.swim.log` and the chain.
  - **`d` in `swim watch`** says what would move; `d` again within 5s
    trashes the lane.
  - **The hub's Trash button** (operators, with `trash` in `swim.yml`'s
    `headless.allow`) opens a confirmation page; the command is pinned to
    the lane's job, so a lane rewritten since is never trashed by an old
    click.
- **`swim watch` shows a timer on every row.** A running (or waiting)
  lane shows how long its round has been going, `1m5s`; a finished one,
  how long ago it ended, `3h ago`. A running batch shows its time since a
  worker took it, and a waiting one how long ago it was queued.
- **Tool caches between rounds, `# Cache:`.** A lane that declares
  `# Cache: terraform @infra/.terraform.lock.hcl` (or `npm`, `pip`, `go`)
  gets the tool's own download cache (`TF_PLUGIN_CACHE_DIR`,
  `npm_config_cache`, `PIP_CACHE_DIR`, `GOMODCACHE`) pointed at a per-repo
  directory under `~/.cache/swim`, so `terraform init`, `npm ci` and
  `pip install` skip what they already have.
  - **The tool still checks the cache against the lockfile**, which is
    required and pinned in the execution manifest: changing it stales
    approvals, and a lane whose lockfile is missing doesn't start. Installed
    trees and build caches (`node_modules`, `GOCACHE`) aren't cached.
  - **Directories are yours, mode 0700:** a cache another user can write,
    or a symlink, is refused; `cache_dir` under `/tmp` gets your user id.
    Clones and worktrees of one repo share its cache. `cache_dir` and
    `cache_slug` go in `config.yml` only.
  - **Two lanes' `terraform init` take turns** on the plugin cache, which
    isn't safe for concurrent inits; other steps and presets share.
  - **`swim cache ls|rm|prune|path`**: sizes, last use and lanes
    (`swim.cache/v1`); runs trim the repo to `cache_max` (10GB), least
    recently used first, never a cache a running lane holds. `swim cache
    path --ci` gives a CI cache step its directory and keys. `swim plan`
    shows each lane's caches; `swim lint` (`cache`, `cache-bypass`) and
    `swim doctor` (`cache-dir`, `cache-unused`) check them.
- **`swim clean` and `swim prune`: a recoverable clean slate, and a
  deliberate purge.**
  - `swim clean` archives every lane's finished rounds under one name
    (`clean-YYYYMMDD-HHMM`), leaving running and Always lanes, then asks,
    each defaulting to no, whether to remove the waiting queue batches and
    whether to move the lane scripts aside to `.swim/cleaned/<stamp>/`
    (`swim clean --restore STAMP` puts them back). It never deletes; without
    a terminal it archives and answers no. `-n`, `-o yaml`
    (`swim.clean/v1`), `--all`, `--include-always`.
  - `swim prune --older D` deletes leftovers past the cutoff for good:
    moved-aside scripts, consumed queue entries, old run stages, and with
    `--logs` archived logs. It needs `prune` typed (or `--confirm`), never
    touches the record, and logs it in the chain first, so `swim verify`
    reports pruned rounds as pruned, not missing.
  - `swim doctor` says when `swim prune --older 30d` would free over 100 MB.
- **Finally lanes and jobs: run once others end, however they ended.**
  `# Finally: 1 2 3` starts a lane once those lanes have passed, failed,
  been skipped or interrupted (where `After:` needs them to pass): cleanup
  and run reports as ordinary pinned, approved, logged rounds.
  - Naming an upstream lane pulls the finally lane into the run, a queued
    batch or a schedule (`--no-finally` leaves it out); `swim all` runs it
    like any pending lane.
  - It sees `SWIM_FINALLY_<N>` (pass, fail, skip, interrupted),
    `SWIM_FINALLY_<N>_JOB` and `SWIM_FINALLY`, and SDK lanes get them in
    `hello`; `swim status`, the run record and its chain round entry
    record what it saw.
  - The first Ctrl-C doesn't stop it, and it still starts, within
    `finally_timeout` (default 10m); a second skips or interrupts it.
  - Its result never changes another lane's: the summary reports it on its
    own line, `run-done` counts it, and the run exits 89 when an ordinary
    and a finally lane both failed.
  - `# swim job 1.9 finally` does the same inside a lane of jobs.
  - `swim plan` marks finally lanes `[F]` (`finally:` in `--yaml`).
- **`swim version` checks for a newer release.** It prints the version,
  then the newest `v0.<date>.<n>` tag in swim's repo (from `git ls-remote`,
  with your own git credentials: https, then ssh, never prompting, a few
  seconds at most) and how this build stands against it, with the
  `go install` line and `swim changelog --since` when there's a newer one.
  It says why when it can't reach the repo, and always exits 0.
  `swim --version` is unchanged: one line, offline. `SWIM_RELEASES_URL`
  points it at a fork or a mirror.
- **`swim skill`: install the Claude skill for swim.** It prints where the
  skill lives (`skills/swim`, at this binary's release tag, else main) and
  the line to paste into a Claude Code session to install it.
- **`swim schedule`: a batch that starts no earlier than a time.** `swim
  schedule 1 3 22:00 --approve FLAG` (or `swim queue add ... --at 22:00`)
  queues the batch pinned and approved now; no worker claims it before
  22:00, and from then on it runs as soon as its lanes are free (late is
  late, not cancelled).
  - **Times:** `13:30`, `1330`, `1:30pm`, `tomorrow 06:00`, `2026-10-20
    13:30`, `+2h`, in `--tz`, `schedule_tz` or the machine's zone. A bare
    past time means tomorrow; anything ambiguous, or a time the clocks skip,
    is refused. The reply says it in both zones: `Lane 1 scheduled for
    13:30:00(CDT) 18:30:00(UTC)`, then warnings (no worker running, a batch
    ahead of it, a dependency that hasn't passed).
  - **Dependents wait with it:** a batch queued later whose lanes depend on
    a scheduled lane is held until the scheduled batch has run.
    `swim run N` of a scheduled lane is refused; `swim all` leaves it.
  - **Recorded and signed:** the start time is in the queue signature (a
    worker refuses a signed batch whose time was edited), `.swim.log`
    (`schedule`, `schedule-start` with how late, `schedule-rm`), and the
    run's chain attestation (`scheduled_for`, which `swim verify` checks).
  - `swim status`, the watch screen and the hub show scheduled and held
    lanes and batches (and recurring schedules); `swim plan` marks a
    scheduled lane `[@] scheduled` and one waiting for it `[=] held`;
    `swim schedule list`, `swim schedule rm` (a dependent then skips saying
    the schedule was removed); `swim doctor` warns when batches are
    scheduled and no `swim watch` runs.
  - `-y` is refused for a schedule: name each guard with `--approve`.
  - **Recurring: `swim schedule 4 06:00 --daily [--until DATE | --for D]`.**
    A template in `.swim/queue/recurring/`; each day a worker makes that
    day's occurrence once, as an ordinary scheduled batch. 06:00 stays
    06:00 local across DST. It expires (30 days by default, never past
    `lookup.yml`'s `standing.max`); a lane whose script or manifest changed
    pauses it rather than run on the old approvals; after downtime only the
    latest occurrence runs (earlier days `superseded`), and a day due while
    the last still runs is skipped (`overlap`). A signed schedule's
    occurrences carry its signature, so standing approvals of whoever
    scheduled it apply; one edited off what was signed is refused. `swim schedule pause|resume`,
    `swim stub`/`swim new` refused on its lanes without `--force`, and
    `swim doctor` warns about paused and expiring schedules.
- **`swim report`: what changed since you last looked,** in one call and
  untruncated: the lanes whose state changed (and what they were), every
  `.swim.log` line and every lane-log line written since the reader's
  earmark, verbatim, and the steps running now.
  - **`--earmark`** then moves the reader's earmark (`--name`, else
    `SWIM_SESSION`, else `default`; `.swim/earmarks/`) to the end of what it
    printed. Earmarks follow rounds, not files, so an archive doesn't lose
    the place; one that does (a round pruned, a log rewritten) says so.
  - **`--wait`** blocks until there's something new: `swim report --earmark
    --wait` is an agent's catch-up loop in one command.
  - Narrow to lanes or jobs, `--no-logs`, `--results` (results without step
    output), `--since T|RUN|JOB`, `--yaml` (`swim.report/v1`), `--earmarks`.
- **Execution manifest:** swim pins what a lane runs before it waits, runs
  a staged copy, and approvals sign the whole operation, not just the
  script.
  - **What's pinned:** the script, every local file it sources or imports
    (by `# Includes:` or a literal path, followed to any depth), its
    sidecar, how it runs (`bash`, its `#!` line, a `swim.yml` runner), the
    policy keys of `swim.yml` and `lookup.yml`'s `require:` rules. Its hash
    is `manifest_sha256`. The env files' hash and the runner binary are
    recorded and checked for the run, but not signed (they differ per
    machine).
  - **Staged runs:** before any lane waits, `swim run` copies its pinned
    files to `.swim/runs/<run>/<N>/` (read-only) and runs that copy, from
    the repo root. A sourced repo file is read from the stage. The copy, and
    a queued batch's script, manifest and env pins, are checked again right
    before exec: `blocked: manifest changed since staged (...)`, or skipped
    as changed since it was queued. Editing the working tree, even
    mid-round, no longer reaches a running lane. `$SWIM_STAGE` and
    `$SWIM_MANIFEST` name the stage.
  - **Approvals bind the manifest:** `swim approve N` lists what it pins
    and signs its hash (`approvals/lane.N.<sha12>.m<manifest8>.signature`).
    A change to any of it stales the approval, and the guard, `swim
    approve` and `swim verify` say why: `stale: includes lib/orders.sh
    changed`, `stale: config policy changed (swim.yml: ...)`.
  - **Questions come from swim:** each lane process gets its own approval
    token. The question names the lane, job, guard, manifest and evidence
    the launcher knows, then `the lane says: <action>` (control characters
    dropped). A request without a token, or for a flag neither the script
    nor a file it includes declares or uses, is refused and logged (a script
    that names a guard's flag at run time, `guard "$F"`, may ask for any). Each approval is used once, by
    the lane it was given to (`_mark ... --request ID`); a replay is
    refused.
  - **Observed facts:** `# Observe: NAME = <command>` and `# Observe: path
    CMD ...` declare facts swim can't pin. They're read when a guard is
    approved (shown with the question) and again just before it returns, and
    a change ends the round as drift: `drift: aws_account was 1234... at
    approval, now 2109...`. The approve entry records them.
  - **Chain:** round and approve entries carry `manifest_sha256`, status
    records it, the round header shows it, and `swim verify` checks a run's
    staged copy while it's kept. `swim doctor --fix` prunes staged copies
    older than `outputs_keep` (`stages-old`).
  - **Lint:** `source` (error) for a bash lane that sources a local file
    `# Includes:` doesn't list, or a path built at run time; `observe`
    (warn) for a probe that looks like it writes. SDK guard calls
    (`r.guard("FLAG", ...)`) now count as declared guards.

- **Headless workers, `swim watch --headless`:** the queue worker with no
  terminal.
  - It needs the directory enrolled with a hub, never shows the watch
    screen and never reads stdin. The hub's worker page marks it headless,
    with `-y` in bold.
  - One per repo: `.swim/headless.lock` holds its pid, and a second
    `--headless` exits 2 naming it.
  - `--detach` starts it in the background (`.swim/headless.pid`, output in
    `.swim/headless.log`, rotated at 10 MiB with 3 kept). `--status` says
    whether it runs (exit 3 when not, naming a stale pidfile), and `--stop`
    sends SIGTERM only to a verified `swim watch --headless` holding the lock.
  - `--install systemd|launchd` writes a user unit (`swim-<id>.service`, or
    `com.swim.<id>.plist`) and prints the command to enable it, plus
    `loginctl enable-linger` for systemd. swim never runs that command.
    `-y` needs `--i-mean-it`. `--uninstall` stops the worker and removes the unit.
  - SIGTERM stops it after the batch in hand, as before. SIGHUP reopens its
    log and leaves running lanes alone (the launcher's new `NoHangup`
    option). Told to stop, it exits 0.
- **Hub users and commands (headless stage H2):** people, not shared
  tokens, act on the hub, and a headless worker takes their commands.
  - `swim hub user add NAME --role viewer|operator` (admin; `remove`,
    `list`) prints the user's token once; the hub keeps its hash. Users log
    in at `/login` for a 12-hour session cookie (`HttpOnly`,
    `SameSite=Strict`, `Secure` under TLS) whose forms carry a CSRF token,
    or send the token as a bearer token. The admin token manages users
    and sends no commands.
  - A logged-in user gets controls on a headless worker's page:
    Interrupt (pinned to the run shown), Stop the worker after this batch
    (drain), Run now (pinned to the script and manifest shown) and Log.
    `viewer` may only fetch logs. Each command's outcome (`done`,
    `refused: why`, `expired`) shows next to it.
  - The hub keeps commands in its state (`swim.hub-state/v2`) and only
    relays them: the worker long-polls with its own token, takes each once
    (epoch, seq and recent ids in `.swim/hub.yml`), expires it after
    `headless.command_ttl` (10m, interrupt 2m), checks `swim.yml`'s
    `headless.allow` and the pin, and records it in `.swim.log` and the
    chain (`hub <cmd> by <user> via <url>: <outcome>`; `swim verify`
    accepts the new `hub` entries).
  - `run` must be allowed in `swim.yml` and queues an unsigned batch, by
    `hub:<user> via <url>`, with no approvals of its own. Logs are masked
    again before upload (only the last 8 MiB) and kept in the hub's memory
    only (64 MiB in all, 15 minutes); `headless.logs: off` refuses them.
  - New `swim.yml`-only block (an error in `config.yml`): `headless:
    {allow, logs, command_ttl, approvals, answer_timeout}`.
- **Guards answered through the hub (headless stage H3):** a headless
  worker's guard that nothing else approves waits for an answer on its hub
  page instead of dry-running at once.
  - **Signed** (the default, `headless.approvals: signed`): `swim approve
    --hub WORKER/QUESTION [--decline]`, or *Sign an answer* on the page
    (`ssh-keygen -Y sign` the statement shown and paste the signature).
    The statement (`swim hub answer v1`) names the question, worker, lane,
    job, run, script, manifest, evidence, guard, decision and expiry; the
    worker builds it from its own facts, checks the signature against
    `lookup.yml` as the run started and counts signers until the guard's
    rule is met. The launcher checks the answer again before it grants.
  - **Button** (`headless.approvals: hub`): a hub operator's *Approve*
    counts as their answer, if `lookup.yml` lists them, only for guards no
    `require:` rule names and that need no evidence. Either kind may
    decline; a viewer may send signed answers.
  - **No answer is a decline:** past `headless.answer_timeout` (30m), the
    round's Timeout, or with the run stopping, the guard is a dry run.
  - Recorded as chain methods `hub-signed` (each signer's statement and
    signature) and `hub-button`; `swim verify` checks hub signatures
    (consistent) and the rule (authorized: a button fails any `require:`
    rule for its guard). The answer command itself is a `hub` entry too.
  - The hub's worker JSON gains `pending` (the waiting questions); the
    launcher gains an `Asker` option, used after pre-answers and `-y` when
    there is no terminal.
  - **`swim doctor`:** `headless-service` (running or installed),
    `headless-stale` (a killed worker's pidfile), `hub-unreachable`,
    `headless-hub-answers` (button answers on) and `headless-yes` (`-y` in a
    service file).
- **Jobs:** any lane can be split into jobs, numbered under it and nested
  to any depth (`1.1`, `1.2`, `1.5.9.9`): the lane's header first, then
  each job at a `# swim job N.K` line with its own header and steps.
  - **SDK languages:** a Python, TypeScript or Go lane marks its jobs in
    comments (`# swim job 1.2`, `// swim job 1.2`). Each job runs the
    program; `swim.jobs({...})` (`jobs()`, `swim.Jobs`) calls its function,
    by job id or place under the lane, closest first, and `r.job` (`Job()`)
    is its id. `hello` returns `job_path`.
  - **In order:** jobs with steps run one at a time in file order, and the
    first failure skips the rest. Each has its own log (`agentN.<job>.log`,
    `swim log 1.2`), status and job id. A job with only a header groups the
    jobs under it.
  - **A job id names it and everything under it:** `swim run 1.5` runs
    1.5 and every job under it, `swim run 1.5.9.9` just that one; `After:
    1.5` waits for all of them. `1[5]` works too.
  - **Always per job:** `Always: yes` covers the jobs under it; other lanes
    wait only for a lane's Always jobs, and `swim all` runs those every
    time, plus any job that hasn't passed.
  - **Less ceremony:** swim loads its library and calls `lane_init` and
    `summary` for a job that doesn't; code before the first job runs in
    every job. swim runs the script it generates at
    `.swim/groups/lane.N.<job>.sh`, and the lane's hash is its file's.
  - **Group files:** `lanes.<name>.sh` at the repo root is a lane of jobs
    with a name instead of a number. It can't also have a `lane.N.sh`, and
    `swim new` and `swim stub` refuse it.
  - **Checks:** `swim lint` reports at the file's lines, plus `group` errors
    for the jobs themselves; `swim ci --changed` counts a changed group file
    as a change to its lane.
  - **CI:** `swim ci` gives each job (and each matrix cell) that ran its own
    log section, annotations, job summary row and JUnit suite (`swim 1.2`).
  - **Output:** a job's lines start with its id alone (`[1.3] ...`, not
    `[1] [1.3] ...`), and its banners read `swim 1.3`. Each part of the id
    has its own lane colour: in `1.3` the 1 is lane 1's and the 3 lane 3's.
  - This repo's CI (fmt, lint, test, e2e) is lane 1 in `lanes.ci.sh`, jobs
    1.1-1.4.
- **Always lanes:** `# Always: yes` (or `always: true` in `lane.N.yml`)
  makes a lane standing setup, like a CI `before_script`.
  - **Every time, first:** it never counts as passed, so `swim all` runs it
    every time, and every other lane in the run waits for it to finish.
    Lanes that name it in `# After:` need it to pass; the others start
    whatever its result.
  - **Pulled in by name:** `swim run N` (and `swim ci --changed`) runs the
    Always lanes N names in `# After:`, and no others.
  - **`# Rerun-Children: yes`:** `swim all` also reruns every lane
    downstream of it, at any depth, even if it passed.
  - **Guard rails:** an Always lane waits only on other Always lanes;
    `swim stub` and `swim archive` refuse one without `--force`; `swim lint`
    warns about a guarded round under a Rerun-Children lane (`rerun-guard`).
  - **Plan:** `swim plan` draws Always lanes first as `[*]` and marks
    reruns `rerun (swim N)`; `swim plan --yaml` adds the `always` action
    and `setup` and `rerun_by` fields.
  - This repo's CI uses it: fmt (`lane.1.sh`), lint (`lane.2.sh`) and unit
    tests (`lane.3.sh`) are Always lanes that run first, in that order;
    e2e moved to `lane.4.sh`, and lanes 5 and up are free for rounds.

- **Native SDKs over a lane protocol:**
  - **The protocol:** `swim _wrap` serves `swim.lane/v1` (JSON lines over a
    per-lane Unix socket, mode 0600, with a per-lane token) to lanes in
    other languages. `proto/swim/lane/v1/README.md` is the contract.
  - **The SDKs:** Python (`sdk/python`, `swim-lanes`, `import swim`),
    TypeScript (`sdk/ts`, `@gates-brightly/swim`, runs as is under
    `node --experimental-strip-types`) and Go (`sdk/go/swim`). Each gives
    run, gate, snapshot, evidence, stage, guard, confirm, drift, stop, msg,
    input, output and outcome handlers as calls.
  - **In-process steps:** checks the program runs itself (`with
    r.step(...)`) are logged as `<label> (in-process)`. A failed in-process
    gate ends the round.
  - **Ending the round:** the SDK raises `RoundEnded`, and the program gets
    10 seconds to unwind before it's stopped.
  - **Conformance:** one test round in bash, Python, TypeScript and Go must
    produce the same log.
  - **`# Includes:`** puts local modules in the script's hash, so approvals
    cover them. Lint warns about uncovered local imports (`includes`) and
    about SDK lanes running commands themselves (`sdk-bypass`).

- **Plan approvals (evidence):**
  - **Evidence steps:** `evidence "<label>" cmd` (or `swim evidence` from
    any language) is a check stage step whose output is saved, normalized
    and hashed as the round's evidence. A failure stops the round, like a
    gate.
  - **Approving:** `swim approve N` on a round with evidence steps (or a
    guard whose `lookup.yml` rule has `evidence: true`) shows the latest
    evidence from `swim plan --check` or a run, and signs its hash with the
    script. It refuses when the evidence is missing or stale.
    `--no-evidence` approves the script only.
  - **At run time:** the approval counts only while this run's evidence
    matches. When it doesn't, the log shows `WARN evidence changed since
    approval` and a diff, and the guard falls through to the prompt (which
    shows the evidence) or a dry run.
  - **Rules:** `evidence: true` rules ignore script-only approvals.
    Pre-answers in the live view don't count for those guards.
  - **Watch:** `swim watch` doesn't compare evidence, and logs that it
    didn't.
  - **Records:** round and approve chain entries carry `evidence_sha256`.

- **Matrix rounds:**
  - **Declaring:** `# Matrix: SVC=orders,billing,users` (or `SVC=@file`)
    runs one lane script once per value. Several lines give the cross
    product, and `# Matrix-Exclude:` drops combinations.
  - **Per cell:** each cell gets its own log (`agentN.<cell>.log`), status
    entry, job (`<job>/<cell>`), chain entry and result, and its variables
    as env vars. `$KEY` in `Round`, `Locks` and `Outputs` is substituted.
  - **Scheduling:** `# Matrix-Parallel: N` caps cells at once (each in a
    `--parallel` slot), and `# Matrix-Fail: stop` skips cells not yet
    started after a failure.
  - **Results:** the lane passes only if every cell does. `After: 3[orders]`
    waits on one cell.
  - **Rerunning:** `swim run 3 --failed` reruns only the cells that didn't
    pass, and `swim run '3[billing]'` runs one cell.
  - **Reading:** `swim log 3` lists the cells and `swim log '3[orders]'`
    shows one. `swim status`, `swim plan` and the hub show the cells.
  - **Hashing:** a value file is part of the script's hash, so approvals and
    queue pins cover the list.
  - **Approvals:** each cell asks its own guard questions, naming its cell,
    and `swim approve N --cell orders` limits a signed approval to one
    cell.

- **Pre-approve in the live view:**
  - **Keys:** on a selected lane that hasn't asked yet, `y` pre-approves
    its known guards (`Guards:` and `guard` calls), `n` pre-declines them
    (dry runs) and `u` undoes. Marks show `✓ pre-approved`, `✗ dry run` or
    `? N guards`, and the selected lane's guards show under the panel.
  - **Precedence:** a pre-answer is the prompt's answer, given early.
    `FLAG=1`/`FLAG=0` and signed approvals still come first, and it comes
    before `-y`.
  - **Limits:** it's pinned to the lane's script (a changed script asks
    again) and lasts this run only. Guards named at run time
    (`guard "$FLAG"`) still ask.
  - **Records:** the log says `pre-approved by <user> at <time> in the live
    view`, the chain records method `preapproved`, and unused pre-answers
    are listed after the summary.

- **Read-only preview, `swim plan --check`:**
  - **What runs:** each selected round's `snapshot` and `check` stages run
    for real, all at once. `After:` is ignored and `Locks:` still apply.
    Every round stops where `change` would begin, recorded as `CHECKED`.
  - **What it never does:** approve a guard (not even with the flag set),
    run handlers, write the round log or change lane status. It writes
    `agentN.check.log` and the check fields only.
  - **The plan:** shows `check PASS`, `stops at gate "<label>"`, `check ~`
    for lanes that depend on unrun changes, refused lanes (no stages, or
    compiled), and the guarded changes. It exits 1 if a check didn't pass.
    A plain `swim plan` shows the last checks too, marked stale once a
    script changes. `--parallel N` and `--timeout D` cap it.
  - **Records:** status gets `check_result`, `check_at`, `check_sha256`,
    `check_stop` and `check_guards`, the chain a `check` entry, and
    `swim plan --yaml` a `check` block per lane.
  - **Also:** `swim ci --check` runs a pull request preview. A new lint
    check, `check-unsafe`, flags a destructive-looking command before
    `stage change`.

- **Lane outputs:**
  - **Producing:** `# Outputs: stats.json, pages/` promises files in
    `$SWIM_OUT`, a fresh `.swim/out/<job>/<run>/` per round. `output KEY
    value` records small values.
  - **Checked at the end:** a promised output that's missing fails the
    round, and undeclared files get a `WARN`. Outputs are hashed (a
    directory as a sorted manifest) into status, the END block and the
    chain, and made read-only.
  - **Consuming:** `input N name` (or `swim input` from any language)
    prints the path, or the value, from the round this run waited on.
    `input JOB name` pins an exact round, even from an earlier run. Only
    dependencies (`After:`, config deps) can be read; lint catches an
    `input` of a lane outside them.
  - **Provenance:** the chain records what each round read and its hash,
    and `swim verify` proves it matches what the producer made.
  - **Housekeeping:** `swim doctor --fix` prunes outputs older than
    `outputs_keep` (default 14d) that no lane's latest round uses.
    `swim ci` lists `.swim/out/` among its artifacts.

- **Lane messages (`msg`):**
  - **Posting:** `msg [--progress N/M] "<text>"` in a bash lane, or
    `swim msg` from any language or from a command inside a step, posts the
    lane's status message. The latest wins, and `msg ""` clears it.
  - **Where it shows:** the live view (after the round), `swim status`
    (`message`, `progress`, `message_at`), `swim run --yaml` (`msg` events)
    and the hub, with a progress bar and the message's age once it's a
    minute old.
  - **The log:** `MSG` lines, changes only, at most one per
    `msg_log_interval` (default 10s), plus the latest when a step or the
    round ends. A message posted during a step goes into that step's block,
    and `swim log --yaml` lists it under the step's `messages`.
  - **Clean text:** one line, ANSI and control characters removed, masked,
    at most 120 characters.
  - **Never fails a round.**

- **Outcome handlers (lane script syntax 3):**
  - **Handlers:** a round can define `on_success`, `on_failure` and
    `on_exit`. swim calls them however the round ends: the end of the
    script, a gate, `stop`, a hard failure, a blocked command, Ctrl-C, a
    timeout or a bash error. `on_success` or `on_failure` runs first, then
    `on_exit`.
  - **Context:** handlers get `SWIM_OUTCOME` (pass, fail, stopped,
    interrupted, timeout), `SWIM_STOP_REASON`, `SWIM_FAILED_STEP` and
    `SWIM_FAILED_STAGE`. `reached STAGE` tells whether the round got that
    far.
  - **They run soft:** a failed step, gate or `stop` in a handler never
    stops it. Handler results are logged in their own sections and
    recorded on the END block, in status, `--yaml` and the chain, but never
    change the round's result. A failed round whose `on_failure` failed
    exits 89.
  - **`# Fail: hard`:** the first failed step ends the round and goes to
    `on_failure`. `run --soft` opts one step out.
  - **`# Handler-Timeout:`** (default 5m) is the handlers' budget, starting
    when the round ends.
  - **Interrupts:** the first Ctrl-C runs the handlers, and their steps
    start despite the stop request. The second skips the rest.
  - **Other languages:** lanes name commands with `On-Success:`,
    `On-Failure:` and `On-Exit:` (or sidecar keys). In them `swim gate` and
    `swim stop` end nothing, and `swim run --soft` works in hard rounds.
  - **Syntax:** scripts that use any of this declare `# swim: syntax 3`, so
    an older swim refuses them rather than skipping their rollback. Nothing
    is migrated, and logs stay syntax 2.
  - **Lint and plan:** `swim lint` catches a missing syntax line, near-miss
    names (`on_fail`, `onfailure`, ...) and an `on_failure` that uses a
    guard without `reached`. `swim plan` lists each handler and its guards.

- **Env files:**
  - **What they are:** declarative `KEY=VALUE` files that give lanes their
    environment. They are parsed and never run as shell.
  - **Scopes:** `~/.config/swim/env.swim` holds general keys and
    `[repo PATH]` sections. The repo's git-ignored `.env.swim` holds
    `[lane N]` sections. A committed `swim.yml` can add shared `env:`
    defaults.
  - **Precedence:** the shell, then lane, repo, `swim.yml`, general.
  - **No approvals from files:** a key that is one of a lane's guard flags,
    or matches a `lookup.yml` guard glob, stops the run before any lane
    starts. `swim lint` and `swim doctor` report it too.
  - **Masked and recorded:** file values are masked whatever their names.
    The round header lists the files that applied (`env-files:`), and
    status and the chain record `env_sha256`.
  - **Queue:** a queued lane whose env changed since it was queued is
    skipped.
  - **`swim env [N]`** shows the resolved env, masked, with each key's file
    and line.

- **Lanes in any language:**
  - **Scripts:** a lane's script can be `lane.N.py`, `lane.N.js`,
    `lane.N.ts`, a compiled `lane.N`, a plain bash script or any runnable.
    There is exactly one per lane; two is an error.
  - **Templates:** `swim new N "<goal>" --lang py|js|ts` writes one.
  - **How it runs:** `lane.N.sh` runs with bash as before. Otherwise swim
    uses the `#!` line, then a `runners:` entry for the extension (your
    config's, then the repo's `swim.yml`), then the file itself if it is
    executable.
  - **First run:** for an extension with no runner, at a terminal, swim looks
    up the usual runners on PATH (node, tsx, ts-node, bun, deno, python3,
    ...). It asks which to use and saves the answer as `runners.EXT` for
    the repo (`swim config runners.ts tsx` sets it directly).
  - **No terminal:** in CI or under `swim watch`, a lane with no runner fails
    at start with the exact fix.
  - **Wrapper:** a script that doesn't load the bash library runs inside
    swim's wrapper, which opens and closes its round, exports the `SWIM_*`
    variables and keeps the git shim in front.
  - **In-lane commands:** steps are swim commands: `swim run|gate|snapshot
    "<label>" -- cmd`, `swim guard FLAG "<action>"`, `swim stage`,
    `swim drift` and `swim stop`.
  - **Ending the round:** a failed gate, `swim stop`, a blocked git push or
    the operator's stop ends the program, even if it carries on.
  - **No swim calls:** a program that never calls `swim run` is logged as
    one step, with its whole output masked.
  - **Headers:** header comments may be `#` or `//`. A sidecar `lane.N.yml`
    (round, job, after, guards, locks, timeout, step-timeout) carries a
    binary's header, or overrides any script's.
  - **Lint:** `swim lint` runs its bash-only checks only on scripts that load
    the bash library.
  - **CI:** `swim ci --changed` counts changed `lane.N.<ext>` and
    `lane.N.yml` files.

- **Watch hub:**
  - **`swim hub`** serves a dashboard of many workers, with a page per
    worker and `GET /api/workers` as JSON.
  - **`swim hub enroll URL --token T`** enrolls a directory. Its
    `swim watch` then reports lanes, steps, the live tail of running lanes,
    the queue and recent events every interval, including mid-batch.
  - **Push, not pull:** workers report to the hub, so they can sit behind
    NAT or a firewall. A restarted hub fills back up as workers report, and
    silent workers show as stale.
  - **Managing it:** `swim hub leave` and `swim hub status`.

- **Chain of custody:**
  - **What's recorded:** every approval and every round goes into
    `.swim/chain.jsonl`. Each entry is signed with the runner's SSH key and
    hash-linked to the one before it.
  - **Rounds** record the script's hash, the hash of the round's log
    section, the result and the approvals used. Scripts that ran are kept
    by hash under `.swim/scripts/`.
  - **Approvals** record who approved and how: signature, prompt, flag,
    `-y` or queue.
  - **`swim verify`** proves nothing was edited, removed or reordered.
    `swim history` shows the chain by approver.
  - **Config:** `signing_key` and `signing: auto|required|off`. Signing
    uses `ssh-keygen -Y` and never prompts; use ssh-agent for keys with a
    passphrase.
- **Approving (`swim approve`, `swim revoke`):**
  - **One script:** `swim approve N --guards FLAG` approves that exact
    script. Co-approvers run the same command.
  - **Standing:** `swim approve --auto USER` approves every job USER queues,
    never consumed. `swim queue add` signs batches with the queuer's key, so
    "queued by USER" is proven, not claimed. Script and standing approvals
    count together.
  - **Withdrawing:** `swim revoke N` or `swim revoke --auto USER`.
  - **Storage:** approvals are signed, hash-linked logs of approve and
    revoke events in committed files under `approvals/`. Their order is the
    attestation, like commits. `swim approve` lists them.
  - **Unattended:** `swim watch --approved` runs fully approved scripts by
    itself.
  - **Pinned scripts:** queued batches now pin each lane's exact script
    hash. A lane edited after queueing is skipped.
- **`lookup.yml`:** names SSH keys (users with any number of keys), groups
  users (groups can nest), and sets `require` rules such as "`FIN_ALLOW_*`
  needs 2 of platform-admin". `swim keys` shows it.
- **`swim wait`:** blocks until swim is idle and nothing new is about to
  start: no lane running, no swim run waiting, no batch queued or running.
  `--timeout D` gives up after D.
- **`swim history` reads like `git log`:** newest first, each entry with
  its hash, signer, date and what happened, paged on a terminal.
  `--oneline`, `-n N` (or `-N`) and `--reverse` work as in git.
- **`swim queue 1 2 3 --wait`:** `swim queue N ...` is short for
  `swim queue add N ...`, and `--wait` blocks like `swim wait`, then exits
  with the batch's result. `--timeout D` gives up after D (exit 1), leaving
  the batch queued.
- **Security regression suite:** `internal/e2e/security_test.go` checks
  the path from `guard` to the command it protects, using a stand-in that
  touches a marker file.
  - **Passing today:** with no approval, a declined approval, a stop or the
    round's `Timeout:` while waiting, an approval for another script, or a
    script edited after approval, the command doesn't run. A round's chain
    entry names its script, approval and result. A lane with no `guard`
    gets no approval entry. A planted secret stays out of the log,
    `.swim.log` and status.
  - **Recorded as it is:** `swim verify` still passes a chain re-signed
    with another key, with only a warning.
  - **Known gaps:** each one is a subtest that skips with `gap: T<n>`,
    naming the task in `tasks.md` that will close it.
- **Fault-injection, property and fuzz tests (T7):**
  - **Failpoints:** a swim built with `-tags swimfailpoint` stops or kills
    itself at a named point (`SWIM_FAILPOINT=step.after-exit:pause`): after
    a step's command exits but before its result is written, after a queue
    claim's rename, halfway through writing `status.yml`, and halfway
    through a chain append. A normal build has no hooks.
  - **`TestFault*`** (`internal/e2e/fault_test.go`) crash the whole worker
    at each point and restart with the normal swim: a cut-off step is
    recovered as failed, never passed, and never rerun on its own; a claim
    whose worker died is orphaned and not run by the next `swim watch`
    until requeued; `status.yml` stays whole; a cut chain is refused until
    `swim chain repair`. `TestFaultManyWatchers` runs 20 batches through
    four `swim watch` processes: each runs once, and lanes sharing a lock
    never overlap.
  - **`TestSchedulerProperties`** runs random lane graphs (up to six lanes,
    `After:`, locks, failures; `SWIM_QUICK_COUNT` graphs, 15 by default)
    and checks, from timestamps the lanes write: no lane starts before its
    `After:` lanes finish, lock holders never overlap, a failure is never
    reported passed.
  - **Fuzz targets** for the lane log parser, the script header parser,
    the chain line decoder and the lane socket's framing, with seed
    corpora under each package's `testdata/fuzz`.
- **Lane state transitions are checked (T12):** every write of
  `status.yml` goes through `status.Transition`: a running round ends only
  as passed, failed or interrupted; a lane waiting for its turn never goes
  back to idle; a finished round's result never changes in place (a
  failure never becomes a pass without a new round). An illegal move
  panics in tests and the e2e suites (`SWIM_CHECK_STATES=1`); elsewhere the
  write goes through. `swim status --rebuild` is the one unchecked writer.

- **`swim log` shows the step running now:**
  - **Why:** a step's block reaches the log file only when the step ends.
    Now `swim log N` (and `swim log JOB`) adds the in-flight step from its
    spool: `RUNNING  <label> (in progress)`, with its command and output so
    far.
  - **`--yaml`:** includes it as a `RUNNING` result with `duration_s`,
    `command` and `output`.
  - **`--raw`** is still the file exactly. `RUNNING` never appears in a log
    file and never counts as a result.
- **`swim log N -f` (`--follow`)** follows a lane like `tail -f`. Log lines
  appear as they are written, and the running step's output streams live
  rather than when the step ends.
- **`swim log N|JOB --until-done`** follows until the round ends, then exits
  with the round's status: other services can wait on a job and get its
  result. On an idle lane it prints the last round and exits with that.

### Fixed

- **A command that only looks no longer rewrites status.yml.** Status
  updates that change nothing (swim report and swim status refresh it)
  leave the file alone, so its `updated_at` (swim status's "updated") is
  when status last changed. Before, a peek could rewrite the file with a
  new timestamp, once the clock had ticked past the last write.
- **swim watch no longer starts batches that share an Always lane side
  by side.** Lanes queued as separate batches that each pull in the same
  Always lane (`After: 1`, as this repo's CI lanes do) all started at once:
  the first ran lane 1, the rest found it already running, failed in 0s
  and skipped their lanes, though each `swim queue add` had exited 0. A
  batch now waits until every lane it runs is free, including the Always
  lanes it pulls in (`launcher.PulledIn`).
- **A trashed lane says so.** `swim queue add`, `swim run` and the watch's
  `q` on a lane `swim trash` moved aside now say "swim 5 was trashed (...);
  swim trash --restore STAMP puts it back", instead of asking for a
  `Round:` line.
- **Crashes no longer leave state half-written, or lanes stuck running**
  (T6):
  - **Durable writes:** `status.yml`, queue entries (added, claimed,
    consumed), approvals files and kept scripts are written to a temporary
    file, synced, renamed into place, and their directory synced; a new
    chain syncs its directory too. A power cut leaves the old file or the
    new one, never an empty one. (On macOS this is `fsync`, not
    `F_FULLFSYNC`: the drive's own cache can still lose the last writes.)
  - **Lane claims:** `lane_init` claims a lane under a lock
    (`.swim/laneN.claim`), so of two processes starting one lane at once
    exactly one wins, and records the process's start time with its pid. A
    pid the kernel has since given to another process no longer counts as
    the lane. `.swim/laneN.pid` still holds the bare pid, so an older swim
    reads it as before.
  - **Dead lanes are ended:** `swim run`, `swim all`, `swim ci` and `swim
    watch` start by ending the round of any lane `status.yml` says is
    running but whose process is gone (SIGKILL, a crash, a reboot): the
    cut-off step is recovered, the log gets `END INTERRUPTED` (exit 137),
    the chain a round entry, and the lane is `interrupted`, not
    `running?` until someone runs `swim status --rebuild`.
  - **Queue workers on other hosts:** a claimed batch records the worker's
    start time, a heartbeat and its lease (`queue_lease`, default `2m`;
    new config key), and `swim watch` renews the heartbeat every third of
    the lease while the batch runs. From another host, a claim whose
    heartbeat is older than its lease shows as orphaned, so `swim queue
    requeue` and `rm` work there; claims by an older worker (no heartbeat)
    still show as running. On the worker's own host, a reused pid no longer
    keeps a dead worker's claim running.
- **A lane killed where nothing reaps orphans no longer stays running:**
  in a container whose pid 1 isn't an init, a killed lane's process stays
  a zombie, which swim counted as alive: `swim run` and `swim watch` never
  ended its round, and the lane refused to start again. A zombie now counts
  as dead.
- **A stray `== END` line ends nothing:** a log with an END and no ROUND
  before it read as a finished round that was never found (found by
  fuzzing the log parser).
- **The chain of custody accepts entries of any size:** after an entry over
  256 KiB (a round with thousands of outputs), every later append failed,
  so nothing more was recorded.
  - Appending finds the previous entry however long it is, and `swim
    verify` and `swim history` read entries without a length limit.
  - Each entry is synced to disk as it's written.
  - **`swim chain repair`** (new): a write cut short leaves a last line with
    no newline, and swim refuses to append after it. Repair moves that line
    to `.swim/chain.tail-<time>.bak`, truncates the chain there and appends
    a signed `repair` entry with the removed bytes' size and SHA-256.
    `swim history` shows it and `swim verify` notes it.
- **Two swims in one repo:** a lane another swim was running (a concurrent
  `swim run`, or `swim watch` running a queued batch) showed as idle in this
  one's live view. It now shows as `running elsewhere` with its round and
  elapsed time, then its result, and is never counted as part of this run.
- **A time limit stops what the step started:** a step stopped by
  `--timeout`, `Step-Timeout:` or the round's `Timeout:` could leave its
  children running (`bash -c 'server & tests'` left the server). Each step
  now runs in its own process group, which gets SIGTERM, then SIGKILL after
  5s.
  - Force quit reaches the group too: swim step passes the SIGINT on, and
    the kill finds the group in `<log>.partial.pgid`, which swim step keeps
    while the step runs. A step whose swim step was killed some other way is
    killed when its log is next written.
  - What a step leaves running when it ends on its own is left alone.
  - A step whose stdin is the terminal (a lone lane run from a terminal)
    stays in the lane's group so it can read the keyboard; a limit stops only
    its own process, as before.
- **No step runs after a failed gate in a wrapped lane:** a lane in another
  language (or a bash script without the library) could start its next
  step, or run its next line, in the moment between its `swim gate` failing
  and swim's wrapper stopping it.
  - The in-lane command that ends the round (a failed `swim gate` or `swim
    evidence`, `swim stop`, a refused step) no longer returns to the program
    that ran it: it waits for the wrapper to stop the program.
  - Once the round's end is claimed, `swim run`, `gate`, `snapshot`,
    `evidence` and `guard` refuse to run (exit non-zero, nothing logged),
    and the lane socket answers `run`, `gate`, `snapshot`, `evidence`,
    `begin_step`, `guard` and `confirm` with `round_ended` without running
    them, even for a program that ignores its stop.

## 7.20261013 (v0.7.20261013, 2026-10-13)

Everything since `6.20261012`. Breaking version 7 is a deliberate bump.
Lane scripts, logs and state are the same as in 6. Install it with
`go install github.com/gates-brightly/swimlane/cmd/swim@v0.7.20261013`.

### Upgrading

- **Repos locked at 6 (or earlier) refuse to run until upgraded.** Nothing
  about lane scripts, logs or state changed, so upgrading is just the lock:
  let running lanes finish, run `swim lock --upgrade`, and commit
  `.swim.lock`. CI jobs read the committed lock too, so they need that
  commit before they run.

### Added

- **`swim watch -y`:** the worker approves every guard in every batch it
  runs, beyond what the batch approves:
  - **Logged:** steps read `approved by swim watch -y (<user>)`. The
    consumed entry records it as `approved_by`, and `.swim.log`'s
    `queue-run` event says so.
  - **Batch approvals keep their own credit:** `--approve FLAG` and the
    batch's own `-y` are still credited to the person who queued it.

## 6.20261012 (v0.6.20261012, 2026-10-12)

Everything since `5.20261012`. Breaking version 6 only because this is the
third release on 2026-10-12. Lane scripts, logs and existing state are the
same as in 5; the queue adds new files under `.swim/queue/`. Install it with
`go install github.com/gates-brightly/swimlane/cmd/swim@v0.6.20261012`.

### Upgrading

- **Repos locked at 5 (or 4) refuse to run until upgraded.** Nothing about
  lane scripts, logs or state changed, so upgrading is just the lock: let
  running lanes finish, run `swim lock --upgrade`, and commit `.swim.lock`.
  CI jobs read the committed lock too, so they need that commit before they
  run.

### Added

- **A queue, and a worker that runs it:**
  - **`swim queue add [N|JOB ...] [--approve FLAG]... [-y] [--note …]`**
    queues a batch of rounds to run as soon as swim is idle. Each lane is
    pinned to the job it held when queued, so a lane rewritten afterwards
    is skipped, not run.
  - **Approvals travel with the batch:** `--approve FLAG` approves that
    guard and `-y` approves all of them. An approval must name a guard in
    the batch, so a typo fails when you queue. A queued batch never asks,
    and the worker's own environment approves nothing, so the queue entry
    alone says what may change. The log records which entry approved each
    step and who queued it.
  - **`swim watch [--once]`** is the worker:
    - It waits until swim is idle (no lane running, no other run waiting
      on lanes), then runs the oldest batch.
    - It consumes the entry when the run ends: the entry moves to
      `.swim/queue/done/` with its run id, exit and each lane's result.
    - Ctrl-C stops the batch gracefully and exits. Batches still waiting
      stay queued.
  - **Safe to run several workers:** an entry is claimed by atomically
    renaming it, so no two workers ever claim the same one. A worker lock
    keeps one batch running per repo.
  - **Managing it:** `swim queue` lists the queue (`--yaml` too), and
    `show`, `rm` and `requeue` manage entries, including orphaned ones whose
    worker died. `swim status` notes waiting batches. `.swim.log` records
    `queue`, `queue-run` and `queue-done` events.

## 5.20261012 (v0.5.20261012, 2026-10-12)

Everything since `4.20261012`. Breaking version 5 only because this is the
second release on 2026-10-12. Lane scripts, logs and swim's state are the
same as in 4. Install it with
`go install github.com/gates-brightly/swimlane/cmd/swim@v0.5.20261012`.

### Upgrading

- **Repos locked at 4 refuse to run until upgraded.** Nothing about lane
  scripts, logs or state changed, so upgrading is just the lock: let running
  lanes finish, run `swim lock --upgrade`, and commit `.swim.lock`. CI jobs
  read the committed lock too, so they need that commit before they run.

### Changed

- **Approvals in the interactive view happen on the lane's own view:**
  - **No more takeover:** a lane waiting for approval no longer covers the
    screen. A banner names it (`▶ swim 2 needs approval · Enter to view it
    and answer`).
  - **Enter** opens the lane waiting longest: its log, with the question
    underneath, where `y` or `n` answers it.
  - **Moving around:** `←`/`→` still visit other lanes and `Esc` returns to
    all lanes; the question waits until answered.
  - **Several at once:** more than one lane can wait. Outside the
    interactive view, questions are still asked one at a time.

### Added

- **`swim run|all … -y` (`--yes`)** approves every guard whose flag isn't
  set without asking, terminal or not:
  - **Logged:** each approval is recorded as
    `approved by swim run -y (<user>)`, and as an `approved` event in
    `--yaml`.
  - **Limits:** `FLAG=0` still declines, and `-y` with `--no-prompt` is
    refused.

## 4.20261012 (v0.4.20261012, 2026-10-12)

Everything since `4.20261009`. Install it with
`go install github.com/gates-brightly/swimlane/cmd/swim@v0.4.20261012`.

### Upgrading

- **A guard whose flag isn't set now asks you,** when swim is on a terminal:
  `swim N requires human approval of task: <action>  Y/N [N]`. Before, it was
  always a dry run. In CI, without a terminal or with `--yaml`, nothing
  changes. To keep the old behaviour everywhere, set `approvals: flags` in
  config, or pass `--no-prompt` for one run.

### Added

- **Approval prompts for guarded steps:**
  - **What you see:** `guard FLAG "<action>"` with `FLAG` unset puts the
    question to you. `y` approves, and the log and `.swim.log` record who
    approved. `n`, Enter or Esc declines, and the step dry-runs as before.
  - **Approving up front:** `FLAG=1` still approves without asking (CI,
    scripts). `FLAG=0` declines without asking.
  - **Parallel lanes** queue their questions through swim run, one at a
    time. In the interactive view the question is a box over the screen;
    otherwise it is a line below the output, with lane output held until
    you answer. The lane's panel row shows `awaiting approval`.
  - **No stuck questions:** Ctrl-C at a question declines it and stops the
    run. A stop request or the round's `Timeout:` ends the wait. A lane run
    with `bash lane.N.sh` asks on its own terminal.
  - **Switching it off:** `--no-prompt`, `approvals: prompt|flags` in
    config, or `swim config approvals flags`.
- **`swim ci --rerun`** also runs rounds that already passed, for a test
  suite written as rounds.

### Changed

- **This repo's CI is swim:**
  - **The workflow:** GitHub Actions runs `swim ci` over committed rounds:
    `lane.1.sh` (lint), then `lane.2.sh` (unit tests) and `lane.3.sh` (e2e)
    in parallel, with settings in `swim.yml`.
  - **Locally:** `make ci` runs the same rounds.
  - **Hermetic tests:** the e2e harness strips an outer lane's `SWIM_*`
    variables, its git shim and CI variables, so the suite can run inside a
    swim lane.

## 4.20261009 (v0.4.20261009, 2026-10-09)

Everything since `v0.2.20261009`. Breaking version 4 (3 went to the retracted,
mis-tagged `v0.3.20261009`; see below). Install it with
`go install github.com/gates-brightly/swimlane/cmd/swim@v0.4.20261009`.

### Upgrading

- **Repos locked at 2 refuse to run until upgraded.** Read this revision, let
  running lanes finish, then run `swim lock --upgrade`.
- **Lane scripts and logs move to syntax 2.** This happens automatically the
  first time any swim command loads the repo. Originals are copied to
  `.swim/migrations/<time>/`, and the migration is recorded in `.swim.log`. A
  running lane's files are migrated after it finishes.
- **A file written in a newer syntax stops swim** with a prompt to update.
- **Ctrl-C is graceful by default.** The first press lets running steps finish
  and starts no new ones. `interrupt: immediate` in config, or
  `--interrupt immediate`, restores the old behaviour.
- **Exit 137 counts as interrupted,** not failed.
- **Scripts that read `status.yml` or the run summary** should allow for the new
  lane states (`starting`, `queued`, `locked`) and the new `BLOCKED` result.
  The `--yaml` output is the stable interface for scripts.
- **On `v0.3.20261009`?** It reports itself as `2.20261009`, so run
  `swim changelog --since 2.20261009` on this build to see what changed for you.

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
- **Interactive lane view** for `swim run`/`swim all` with more than one lane,
  in a terminal:
  - `←`/`→` (or `h`/`l`, `Tab`) focus one lane and show its log for the
    current round; `↑`/`↓`, `PgUp`/`PgDn`, `g`/`G` scroll it
  - type digits then `Enter` to jump to a lane; `f` cycles every lane /
    running / failed; `Esc` returns to all lanes; `?` shows the keys
  - scrolling up pauses following; finishing while you read keeps the screen
    open (`q` closes it)
  - off with `--no-tui`, `SWIM_TUI=0`, `--plain`, `--yaml`, in CI, or for a
    single-lane run
- **Graceful Ctrl-C:** the first press stops every lane at its next step
  boundary (`STOP  interrupted by operator (after: <step>)`), and lanes that
  haven't started are skipped. The second press force quits: SIGINT to each
  lane's process group, then SIGKILL after `interrupt_grace` (5s). A third
  kills at once.
  - SIGTERM stops gracefully and escalates after `term_grace` (10s); SIGHUP
    force quits.
  - A `confirm` prompt or a retry wait is cut short.
  - `swim interrupt N|JOB` stops one lane the same way, from any terminal.
  - Lanes now run in their own process groups, except a single lane reading
    the terminal (for `confirm`).
- **`swim ci [N|JOB ...] [--changed[=BASE]]`:** runs rounds in GitHub Actions,
  GitLab CI or any runner with `CI=true`.
  - Step headers and results stream to the log, each lane's round gets a
    collapsible group, failures and drift become annotations, and there's a
    job summary and optional JUnit report (`--junit FILE`).
  - `--changed` runs only rounds whose lane scripts changed in the push or
    merge request, plus parents that haven't passed.
  - The commit is recorded on each round (`$SWIM_COMMIT`, `status.yml`,
    `.swim.log`). Lanes never get stdin, and swim never prompts.
  - Results stay on the runner: upload `.swim/logs/` as artifacts.
- **`swim.yml`:** a committed file at the repo root with the same keys as a
  `repos:` entry, shared by CI runners and every operator.
- **`swim changelog [-n N] [--since VERSION] [--all]`:** what changed in swim,
  from the changelog built into the binary. It works outside a repo.
- **`swim --version` warns about a mis-tagged build,** one whose tag disagrees
  with its breaking version.
- **`features/`:** specs for planned and shipped features, with their status.

### Changed

- **The pre-start state is now called `starting`.** `queued` now means
  "waiting for a parallel slot".
- **`BLOCKED` is a new result kind.** It counts as a failure in pass/fail
  counts, `failed_steps` and stage results.

### Fixed

- **New job ids never start with 8 digits.** Such an id read as a lane number.

## 3.20261009 (v0.3.20261009, 8cd95a5, 2026-10-09)

**Retracted.** This tag was made without bumping `Breaking`, so the build
reports itself as `2.20261009` and the `.swim.lock` check treats it as
breaking version 2. Its code is the same as `4.20261009`, without the fix for
this mistake. Install `v0.4.20261009` or later. `go.mod` retracts it, so
`@latest` skips it.

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
