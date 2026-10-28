# swim review

*This review applies Mitchell Hashimoto's publicly known engineering values (workflow over technology, developer UX, strong defaults, crisp output, no flaky tests, ship small and polish). It is not a prediction of what he would say.*

## Verdict

The core workflow is right, and it's fast: an AI writes a lane, you approve the exact bytes, swim runs that copy and keeps a record. A plain run takes 36ms, the TTY `--help` is a clean 39 lines, and the README quickstart is tested as written. The problems sit where a new user meets the product's promise. The README's headline tamper story doesn't work as written in a fresh repo. The default staged workspace breaks the most natural lane (`cat deploy.yaml`) and gives no hint why. `swim lint` passes scripts that bash can't parse. Fix those first, then cut surface area.

All evidence below comes from building HEAD `b2ea2e6` (`go build ./cmd/swim`, 0.6s) and running it in a scratch repo with a fresh `$HOME`.

## Findings, by impact

### 1. The README's 90-second tamper story doesn't reproduce in a fresh repo

**Observed.** Following README.md:25-42 with a fresh key in `~/.ssh/id_ed25519`, `swim approve 3 --guards CERT_ALLOW_ROTATE` succeeds and writes a signature, but says:

```
approved lane.3.sh 10bc7d76de5e as your key SHA256:0xoq... (not in lookup.yml: it won't count until it's listed)
  not yet:  CERT_ALLOW_ROTATE: no lookup.yml: approvals need their signers' keys listed there to count
```

The next `swim run 3 --no-prompt` dry-runs instead of printing the README's `APPROVED`:

```
SKIP  replace the live cert (dry run: no lookup.yml: approvals need their signers' keys listed there to count; set CERT_ALLOW_ROTATE=1 to approve)
```

It only works after you hand-write YAML. docs/quickstart.md:139 does it with `printf 'users:\n  me: {keys: ["%s"]}\n' ... > lookup.yml`. No command does this step: `swim keys` can `trust` and `pin`, but it can't add a user. The test that runs the README's code (internal/e2e/quickstart_test.go:15, `markedBlocks`) covers only the `<!-- quickstart -->` block, not the tamper story.

**Why it matters.** That story is the pitch. The first person who tries it gets a SKIP and a YAML file to write by hand.

**Smallest fix.** When a signing key exists and there's no `lookup.yml`, `swim init` (or `swim approve`, on first use) writes one that maps `$USER` to that key, and says it did. Add `swim keys add NAME [KEY]` for teammates. Wrap the tamper story in markers so `TestQuickstartRuns` executes it too.

### 2. The staged workspace breaks the obvious lane, silently

**Observed.** A lane with `run "show config" cat deploy.yaml` and `run "helper" bash scripts/helper.sh`, where both files exist in the repo:

```
$ swim lint 6
no problems found
$ swim run 6 --plain
[6] cat: deploy.yaml: No such file or directory
[6] FAIL  show config (exit 1)
[6] bash: scripts/helper.sh: No such file or directory
[6] FAIL  helper (exit 127)
```

`swim lint --fix 6` adds nothing. The lint check closest to this, `workspace-empty` (internal/lint/checks.go:143), only fires for whole-tree tools like `go vet ./...`. The `source` check covers only `source`.

**Why it matters.** Reading a file next to the script is the first thing anyone writes. The staged copy is a good security default, but right now its cost is a raw ENOENT with no hint that swim caused it.

**Smallest fix.** Have lint flag any literal relative path argument that exists under the repo root but isn't covered by `# Includes:`, and have `--fix` add it (the same machinery the `source` check already uses). At runtime, when a staged step fails, look for a missing path that exists in the repo and print one line: `deploy.yaml exists in the repo but isn't in # Includes: (staged copy)`.

### 3. `swim lint` says "no problems found" when there are problems

**Observed.** (a) A lane with an unclosed `if`:

```
$ swim lint 5
no problems found
$ swim run 5 --plain
[5] /tmp/.../qs/.swim/runs/r-20261028T182031Z-db8b/5/lane.5.sh: line 10: syntax error: unexpected end of file
```

There is no `bash -n` anywhere in internal/lint/. The runtime error also points at the staged copy, not at `lane.5.sh`.

(b) `swim lint 7` with no `lane.7.sh` prints `no problems found` and exits 0. The guard against linting nothing (internal/cli/lint.go:69, `!anyLane`) only applies when no lane exists at all.

**Why it matters.** The CHANGELOG says `swim run` refuses a lane with any lint error, so lint is a gate operators and AIs will trust. A green check that didn't check is worse than no check.

**Smallest fix.** Run `bash -n` on `.sh`/`.bash` lanes and report it as `error [syntax]` with the line in `lane.N.sh`. Make a named lane with no script an error. Rewrite staged-copy paths in stderr to the repo-relative script name.

### 4. Signing defaults disagree with each other, and "BROKEN" means two things

**Observed.** With no key, `swim run` records unsigned entries and prints nothing about it. The next plain `swim verify` fails:

```
BROKEN: 2 unsigned entries: #1, 2. Nothing vouches for them, ...
NOT VERIFIED: consistent: 1 problems (BROKEN, above)
```

After you add a key, those early entries stay BROKEN for good. Verify on my repo listed `BROKEN: 7 unsigned entries: #1 ... 7` on the same screen as `BROKEN: entry #14: swim 3's log ... was changed after the round ended`, which is real tampering. `signing: auto` is the default (docs/trust-model.md:214), and verify is strict by default (:217).

**Why it matters.** The first `swim verify` someone runs after the quickstart without `--allow-unsigned` says the chain is broken. And an operator learns to ignore BROKEN, which is the one word that has to mean tampering.

**Smallest fix.** Use a different word, `UNSIGNED`, at warning severity for entries recorded before the repo's first signed entry. Keep `BROKEN` for link, hash and signature failures. Print one line from `swim run` when it records unsigned: `swim: no signing key; this run is recorded unsigned (swim verify will flag it)`.

### 5. A signature mismatch suggests the bypass

**Observed.** After `install.sh` changes under an approval:

```
SKIP  replace the live cert (dry run: lane.3.10bc7d76de5e.m2312c030.signature approves another version of lane.3.sh (approved 10bc7d76de5e, running 46a3cf670e7b); set CERT_ALLOW_ROTATE=1 to approve)
  to approve: CERT_ALLOW_ROTATE=1 swim run 3
```

This comes from internal/cli/approve.go:52-54, which always appends `set FLAG=1 to approve`.

**Why it matters.** This is the moment the product exists for: what you signed changed. The suggested next step is the unsigned bypass, one copy-paste away. It is recorded, but the advice points the wrong way.

**Smallest fix.** When a signed approval exists but doesn't match, the hint should be `review the change (swim approve 3 shows the manifest), then: swim approve 3 --guards CERT_ALLOW_ROTATE`. Drop the `FLAG=1` hint in that case.

### 6. Bad input gets a wall of text, or the wrong error

**Observed.**

- An unknown flag prints the error and then the whole command manual. `swim 1 --pain` prints `swim run: unknown flag --pain` followed by roughly 100 lines of `swim run --help`, and doesn't suggest `--plain`.
- Typos get no suggestion: `swim histroy` prints `swim: unknown command "histroy"`.
- A typo of 8 or more characters is treated as a job id: `swim changlog` prints `swim run: no lane is task changlog, and no lane holds job changlog`. Main sends any `lane.ValidJobID` string to `run` (internal/cli/cli.go:103-106, 143-147).
- `swim approve 2` with no `--guards` prints the manifest block (`approving manifest 0968...`) before it rejects the arguments.

**Smallest fix.** On a usage error, print one usage line plus `did you mean --plain?` (edit distance against known flags and commands) and `swim run --help for more`. Only take a word as a job id when it's hex or UUID-shaped and some lane actually holds it; otherwise it's an unknown command with a suggestion. Validate arguments before printing anything.

### 7. Output polish: the small things a user sees on every run

- Plural bugs: `1 problems` (internal/cli/verify.go:155) and `attested: swim 2, 1 approvals` (internal/cli/custody_cmds.go:734).
- Every summary prints `stages: snapshot none | check PASS | change none | verify none`. Show only the stages that ran.
- A dry run's per-lane line says `exit 0` (`swim 2 summary: SKIP ... exit 0`, and the table's `exit` column says 0), but the process exits 3. Show the exit code the shell will actually see, or drop the column for SKIP.
- The README quickstart's own lane 2 triggers `WARN  change stage without a snapshot stage before it`. The tutorial shouldn't produce warnings; add one `stage snapshot` line to it.
- `swim doctor` pads its first column to the longest location, an absolute key path, so every row is 120+ characters and wraps badly in an 80-column terminal. Put the location on its own line, and show `~` instead of `$HOME`.
- `swim doctor` doesn't mention that signed approvals won't count without `lookup.yml`, which is the problem in finding 1. Add that check.
- `swim run` in a repo with nothing to run still writes `.swim.lock` (`swim: wrote .swim.lock ...` followed by `nothing pending`). A no-op shouldn't create files.

### 8. `swim --help` changes with whether output is a terminal

**Observed.** On a terminal, `swim --help` prints 39 well-grouped lines (CORE / UNATTENDED / MORE). Piped, `swim --help | less` prints the full AI guide: 2,215 lines and 20k words. This is documented in the help text itself.

**Why it matters.** A person who pipes help to `less` or `grep` gets a different document than the one on their screen. Agents already have `swim guide`.

**Smallest fix.** `--help` always prints the 39 lines. Agents use `swim guide` (or `--help --full`).

### 9. Too many concepts, and two of them reuse words

**Observed.**

- The command list (`swim guide`, COMMANDS) has about 45 entries, including 10 in-lane verbs.
- The guide documents about 18 header keys (`Round`, `Job`, `After`, `Guards`, `Includes`, `Cache`, `Observe`, `Task`, `Timeout`, `Outputs`, `Locks`, `Targets`, `Fail`, `Owner`, `Created`, ...).
- The trust layer alone has `approvals/`, `lookup.yml`, `trust.yml`, `trust.yml.sig`, `~/.config/swim/trust-pins.yml`, `.swim/chain.jsonl`, `chain_anchors`, and four verify levels.
- Two words mean different things in different places:
  - **run** is the command (`swim run`), the run id (`r-20261028T...`), and the in-lane step function (`run "say hello" echo ...`).
  - **stage** is a script phase (`stage check`) and also the workspace (the "staged copy"). Finding 2's confusion lives exactly in that second meaning.

**Why it matters.** Each concept is a thing to learn before the first run, and a word that means two things makes the guide hard to read. T23 (tasks.md) already freezes features and lists cut candidates (`cache`, `earmark`), which is the right move.

**Smallest fix.** Rename the in-lane `run` to `step` (it already wraps `swim step`). Call the workspace "the approved copy". In T23 sessions, record which trust files anyone actually touched.

### 10. Test reliability is good locally but not yet proven in CI

**Observed.**

- Unit tests: 45 packages, all green, 5.2s wall time.
- The e2e suite (`go test -count=1 ./internal/e2e/...`) passed here in 95s on 8 cores. That is one run, so it says nothing about flake rate.
- tasks/T29 is open at P0: "e2e red on main in CI". It lists about a dozen timing flakes fixed one at a time. The 2026-10-27 change to event-based waits (`waitDeadline`, `hold`/`r.release`) is the correct fix for the whole class.

**Why it matters.** A red main trains everyone to rerun until green.

**Smallest fix.** Finish T29 as written: three green pushes on both OSes, plus 20 runs in a row at `GOMAXPROCS=2`. Then add a lint test that bans `time.Sleep` in internal/e2e outside the timing helpers, so the class can't come back.

## What's genuinely good

- **Fast.** A one-step lane runs in 36ms, the binary builds in 0.6s, and unit tests take 5s.
- **The TTY help is a model front door:** grouped, 39 lines, with the core workflow first.
- **The docs are tested.** The README quickstart and docs/quickstart.md run as written in e2e (internal/e2e/quickstart_test.go:44, :71).
- **Several errors name the fix:**
  - `swim approve 2 --guards NOPE` prints `lane.2.sh has no guard NOPE (its guards: HELLO_ALLOW_WRITE)`.
  - `swim new 1 again` refuses with the existing round's name and `--force`.
  - The dry-run line names both hashes (`approved 10bc7d76de5e, running 46a3cf670e7b`).
  - A failing gate shows the command, its stderr, `STOP gate failed`, and the reason again in the summary.
- **`swim lint` catches the template placeholder** that `swim new` leaves behind (`[placeholder]`). It's a good default: you can't ship the scaffold by accident.
- **The trust model is honest.** docs/trust-model.md opens with what swim does *not* protect against and maps each claim to a test.
- **Ruthless cutting is already underway.** The CHANGELOG's Unreleased section removes hub, schedule, plan, pause, modules, the SDKs and more, and `features/` is frozen pending outside users. Keep going.

## What I'd do next (one week)

1. **Make the tamper story true out of the box** (finding 1). Auto-write `lookup.yml` for the local key, add `swim keys add`, and put the story under `TestQuickstartRuns`.
2. **Make lint trustworthy** (finding 3). Add `bash -n`, make a missing named lane an error, and map staged paths back to `lane.N.sh` in errors.
3. **Make the staged copy explain itself** (finding 2). Add a lint finding plus `--fix` for repo paths missing from `# Includes:`, and a one-line runtime hint on ENOENT.
4. **Separate UNSIGNED from BROKEN, and stop recommending the bypass after a mismatch** (findings 4 and 5). Both are small string and severity changes on the trust path.
5. **Do a polish pass, then T23.** Short usage errors with did-you-mean, the plural bugs, the doctor layout, terminal-independent `--help`, and a quickstart with no warnings. Then run the five outside sessions and cut what nobody touched.
