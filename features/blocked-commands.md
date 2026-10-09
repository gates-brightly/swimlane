# Blocked commands: swim never pushes, commits or pulls

Status: proposed

## Summary

Swim never changes a repository's git history or remotes, either through its
own code or through the lanes it runs:

- **Swim's own code** calls git only to read (`rev-parse` today). A test keeps
  it that way.
- **Lanes:** a command that contains a blocked substring crashes its lane. The
  round stops at once and is recorded as `BLOCKED`, whether the command is a
  `run` or a `gate`. The default list is `git push`, `git commit` and
  `git pull`. Repos can add to the list but can't remove the defaults.

```
=== STEP 2026-10-09T15:02:11Z publish results
$ bash -c 'git add .swim.log && git commit -m results && git push'
BLOCKED  publish results (matched "git commit", "git push"; swim never writes to git)
STOP  blocked command: publish results
```

## Motivation

- **Lanes run the operator's credentials.** A lane script is written by an AI
  planner or worker and run by an operator who has push access.
  - A `git push` in a lane publishes work no human reviewed.
  - A `git commit` rewrites the history the planner reads back.
  - A `git pull` changes the code under every other running lane mid-round.
- **None of these belongs in a round.** Committing is the operator's decision,
  made outside swim.
- **One rule beats per-round guards.** Swim's safety rules already put
  destructive steps behind guard flags. These three are refused outright, so no
  round depends on someone remembering to guard them.

## Behaviour

### What is blocked

Matching is on substrings, case-sensitive, after collapsing runs of whitespace
to one space. The defaults can't be turned off:

| pattern | why |
|---|---|
| `git push` | publishes to a remote |
| `git commit` | writes history |
| `git pull` | changes the working tree under running lanes |

Repos can add patterns in config. They can't remove the defaults:

```yaml
defaults:
  blocked_commands: ["git rebase", "git reset --hard", "gh pr merge"]
repos:
  /path/to/repo:
    blocked_commands: ["terraform apply -auto-approve"]   # added to defaults + built-ins
```

`swim config` prints the effective list, marking the built-in entries.

### Where it's checked

1. **Before a lane starts:** `swim run`/`all`/`ci` scans each selected
   `lane.N.sh`, comments excluded, for blocked patterns.
   - A hit fails that lane before it starts: state `failed`, reason
     `blocked: lane.3.sh:41 matches "git push"`.
   - Its dependents are skipped, as for any failure.
   - Nothing in the lane runs, not even its snapshot steps.

   `swim plan` shows the same refusal, so a planner sees it before handing the
   round over.
2. **Each step:** `swim step` checks the command it is about to run. That is
   the argv joined with spaces, including the string inside `bash -c '…'`. On a
   hit:
   - it doesn't run the command
   - it writes the `BLOCKED` line to the log
   - it exits with a reserved code (e.g. 87)

   `run`, `gate` and `snapshot` in the lane library treat that code as a crash:
   they `stop "blocked command: <label>"`. A blocked `run` doesn't just record
   FAIL and carry on.

   This catches commands built at runtime that the scan can't see, such as
   `run "x" bash -c "$CMD"`.
3. **Anything that reaches git** (optional, see open question 1): `swim run`
   puts a `git` shim first on each lane's `PATH`.
   - It refuses `push`, `commit` and `pull` wherever they appear in the
     arguments, so `git -C dir push` and `git -c x=y commit` are caught too.
   - It hands every other call to the real git.

   This catches git run outside `run`/`gate`, e.g. a bare `git push` in the
   script or one inside a called script.

### Reporting

- **Log:**
  - `BLOCKED  <label> (matched "<pattern>"[, ...]; swim never writes to git)`
  - then `STOP  blocked command: <label>`
- **Round summary and status.yml:** the round counts as failed. `failed_steps`
  includes the BLOCKED line, and `exit_code` is the reserved code.
- **`.swim.log`:** the lane's `fail` event detail includes
  `blocked: <pattern>`.
- **`swim run` summary:** the lane shows `FAIL` with `blocked: <pattern>` on the
  line below. That is distinct from an ordinary failure, so nobody retries it
  expecting a different result.

### Swim's own code

Swim runs git only to read. Today that is `rev-parse --show-toplevel`
(`internal/config`), and `rev-parse --short HEAD` plus the branch name
(`internal/step.GitRef`). That stays the rule as features are added: for
example, `swim ci --changed` may read with `git diff --name-only`, but never
writes.

Swim doesn't write to `.git` and doesn't stage, commit, fetch, pull or push.
Writing `.gitignore` in `swim init` is a working-tree file edit, not a git
operation, and stays allowed.

## Design

- **Patterns:**
  - built-ins are a constant in `internal/config` (`BuiltinBlocked`)
  - `Config.Blocked()` returns built-ins plus defaults plus the repo's
    patterns, de-duplicated
  - config validation rejects empty patterns
- **Matcher:** `internal/syntax` (or a new `internal/policy`). It has two jobs:
  - `Match(cmd string) []string`: normalises whitespace and returns every
    pattern hit
  - `ScanScript(src) []Hit`: a line scanner that skips `#` comments and the
    header, and reports line numbers

  `internal/syntax` already reads lane scripts, so the scan lives next to it.
- **Pre-flight:** in `launcher.Select` / `ResolveDeps`, alongside the
  `# After:` resolution. The refused lanes come back as outcomes with a reason,
  in the same way a "dependency failed" skip does today.
- **Step:** `internal/step` gets the effective list from the env var
  `SWIM_BLOCKED`, which `lane_init` exports from swim's config (the newline-
  separated patterns). That way the check works when `swim step` is run
  directly too. With no env var, it falls back to the built-ins.
- **Lane library** (`internal/assets/lib.sh`): `run`, `gate` and `snapshot`
  check for the reserved exit code and call `stop`. It must stay compatible
  with bash 3.2.
- **Shim (optional):** `internal/assets/git-shim.sh`, written to
  `.swim/bin/git` by `lane_init`, which then prepends `.swim/bin` to `PATH`.
  The shim finds the real git by searching `PATH` without `.swim/bin`.
- **Guide:** add a line to SAFETY RULES in `internal/help/guide.txt`: "swim
  never pushes, commits or pulls; lanes that try are stopped (BLOCKED)". Also
  remove anything in other feature specs that would have swim commit (see
  `ci.md`).

## Open questions

1. **Ship the git shim?**
   - For: it closes the obvious gaps that substring matching leaves
     (`git -C . push`, `git $verb`, scripts that call scripts).
   - Against: a shim that sits in front of every git call. Note that
     `.swim/bin` is inside a git-ignored directory, so it doesn't dirty the
     repo.
   - Recommend shipping it, with the substring check as the documented
     contract and the shim as defence in depth.
2. **Other ways to write to a remote.** Patterns are substrings, so
   `git push-mirror`-style aliases or `hub`/`gh` commands aren't caught unless a
   repo adds them. Should the built-ins include `gh pr merge`, `gh repo sync`
   and `git merge`? Recommend keeping the built-ins to the three asked for and
   showing examples in the docs.
3. **Matches inside quoted data.** `echo "never git push"` matches too. That
   leans safe, and a lane can rephrase. Should there be an escape hatch? Not
   recommended: a lane that needs the literal text can split it,
   e.g. `"git ""push"`. That's deliberate friction.
4. **Exit code.** Pick a code nothing common uses. 87 is suggested, distinct
   from 126/127 and signal codes. Document it in the log grammar.

## Testing

- **Unit:**
  - `Match`: whitespace collapsing; multiple hits; case sensitivity; built-ins
    always present even when config tries to override them
  - `ScanScript`: hits in comments are ignored, line numbers are right, the
    header is skipped
  - config merging and validation of `blocked_commands`
- **Go e2e (`internal/e2e`):**
  - a lane with `run "x" git push` fails before starting, its dependents are
    skipped, and its log has no ROUND START
  - `run "x" bash -c "$CMD"` with `CMD='git commit -m x'` set at runtime:
    BLOCKED at the step, the round stops, later steps don't run
  - a blocked `run` (not `gate`) still stops the round
  - a repo-added pattern is honoured
  - `swim plan` shows the refusal
  - with the shim: `git -C . push` in the script body is refused, and
    `git status` still works
- **Swim's own code:** a test parses `internal/**/*.go` for `exec.Command("git", …)`
  and asserts each call's first argument is on a read-only allowlist
  (`rev-parse`, and later `diff`, `log`, `ls-files`). Adding a write is then a
  deliberate, reviewed change to that test.
- **Scenario (`e2e/scenarios/blocked`):** a small DAG with one lane that tries
  each built-in through `run`, `gate` and `bash -c`. Every blocked lane must
  stop with BLOCKED and the reserved exit code, its descendants must be
  skipped, and the scratch repo's `git log` / `git status` must be unchanged
  afterwards.

## Out of scope

- A general sandbox for lane commands (network or filesystem policy). Guard
  flags and the safety rules cover destructive operations case by case. This
  feature is one hard rule about git.
- Blocking git writes made by tools the lane calls, e.g. a deploy tool that
  commits internally, beyond what the shim catches.
