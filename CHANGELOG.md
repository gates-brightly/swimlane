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
1. `make next-tag` prints the next tag, `v0.<today>.<index>`.
2. Rename `## Unreleased` to `## 0.<date>.<index> (v0.<date>.<index>, <date>)`,
   and start a new empty `## Unreleased` above it.
3. Commit, then run `make release V=v0.<date>.<index>`. It refuses to tag
   unless the date, the index, the changelog and a clean tree all agree.

## Unreleased

### Security

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

### Added

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

### Fixed

- **Two swims in one repo:** a lane another swim was running (a concurrent
  `swim run`, or `swim watch` running a queued batch) showed as idle in this
  one's live view. It now shows as `running elsewhere` with its round and
  elapsed time, then its result, and is never counted as part of this run.

### Changed

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
