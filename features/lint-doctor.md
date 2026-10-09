# swim lint and swim doctor

Status: shipped (4.20261009)

## Summary

Two read-only checkers that catch, before a run, the silent misconfigurations
that otherwise only show up as a confusing failure:

- **`swim lint [N|JOB ...]`** checks lane scripts: their headers, the lane
  library rules, and the dependency graph.
- **`swim doctor`** checks the environment: the binary, PATH, config,
  `.gitignore` and the repo layout.

Both print findings as `error`, `warn` or `info`, with the file and line and a
fix. They exit 1 on any error, and with `--strict`, on warnings too. Both
support `--yaml` (see [yaml-output.md](yaml-output.md)).

```
$ swim lint
lane.2.sh:5   error  reads .scenario/dag/combined.html, which lane.1.sh writes, but has no `# After: 1`
lane.7.sh:31  error  uses `set -e`; failed checks must keep going (use gate for stops)
lane.9.sh:12  warn   `mapfile` is bash 4; macOS bash 3.2 can't run it
lane.4.sh     warn   no `summary` at the end; the round's final state won't be recorded
2 errors, 2 warnings
```

## Motivation

Each of these happened during this project, and none of them produced a clear
error:

- **No dependency declared:** lanes 1 and 2 ran in parallel while lane 2 read
  lane 1's output, and lane 2 failed on a missing file.
- **Stale `.gitignore`:** `next[0-9]*.sh` instead of `lane.[0-9]*.sh`, so lane
  scripts showed up in `git status`.
- **Wrong platform:** a `swim` binary built for macOS ran in a Linux container,
  failing with `cannot execute binary file: Exec format error`.
- **Wrong `swim` first on PATH:** PATH order chose `~/.local/bin/swim` (the
  `make link` symlink) over `~/go/bin/swim`.
- **Too few lanes configured:** scripts existed beyond the configured lane
  count, so `no swim 5: lanes are numbered 1..4`.
- **A comment that looked like a marker:** a lane's own code contained
  `# swim:stub`, and a naive scan treated it as a stub.

## `swim lint` checks

Each finding carries a code in brackets (`[set-e]`), which disable comments
name.

| check | code | level |
|---|---|---|
| `# Round:` missing | `round` | error |
| a template placeholder left in (`{{...}}`, `<round goal>`, the template's "replace with a read-only describe/list command" step) | `placeholder` | error |
| `# After:` refers to an unknown lane or job, or the lane itself (via `launcher.ResolveDeps`) | `after` | error (`swim run` refuses these too) |
| lanes waiting on each other in a cycle (`# After:` plus config `deps`) | `cycle` | error (`swim run` refuses these too) |
| `set -e` / `set -o errexit` / `set -euo pipefail` / a `-e` shebang | `set-e` | error |
| no `lane_init`, `lane_init` without a number, or with another lane's number | `lane-init` | error |
| `summary` missing or not last (`summary` then `exit` is fine) | `summary` | warn |
| an unknown stage name (`stage deploy`; the round stops there at runtime) | `stage` | error |
| stages out of order, repeated, or `change` before any `snapshot`/`check` | `stage-order` | warn |
| bash 4 features: `declare -A`, `mapfile`, `readarray`, `${x,,}`, `${x^^}`, `\|&`, `&>>`, `coproc`, negative indexes, namerefs | `bash4` | warn |
| a guard flag used in `guard` but not listed in `# Guards:` | `guard-unlisted` | warn |
| a flag listed in `# Guards:` that no `guard` uses | `guard-unused` | warn |
| a destructive-looking command (`delete`, `destroy`, `drop`, `rm -r`) outside the `then` branch of an `if guard ...` block (or `guard ... &&`) | `destructive` | warn |
| a blocked command (see [blocked-commands.md](blocked-commands.md)), via `policy.ScanScript` | `blocked` | error (`swim run` refuses these too) |
| `echo`/`printf` of a variable named in `secret_env`, or matching the `secret_env_auto` patterns (see [secret-masking.md](secret-masking.md)) | `secret-echo` | warn |
| a header value swim ignores (`lane.Info.Problems`: a bad `Timeout`, `Step-Timeout` or `Locks` name) | `header` | warn |
| **uses a path another lane writes without either waiting on the other** (heuristic: a path written in lane A with `> path`, `-o path`, `cp`/`mv … path` or `tee path`, appearing in lane B's code, with no dependency either way) | `cross-lane` | info |
| the script's syntax is older (migrated by the next swim command) or newer than this swim reads | `syntax` | warn / error |
| a malformed disable comment (no reason: error; unknown code: warn) | `lint-ignore` | error / warn |

The cross-lane check is a heuristic and can't be complete. It reports what it
saw (`uses shared/report.json, which lane.2.sh:23 writes, but doesn't wait for
swim 2`) so a person can judge.

The scans work on a view of the script with comments, quoted strings and
heredoc bodies told apart (`internal/lint/shell.go`), so prose like
`# Do not use set -e` or a step like `run "x" bash -c 'mapfile ...'` (a
sub-shell, not the lane script) doesn't trip them.

### Disable comments

```
rm -rf "$TMP"   # swim:lint-ignore destructive scratch dir this round made   (this line)
# swim:lint-ignore bash4 runs only on the Linux CI box                      (the next line)
# swim:lint-ignore-file cross-lane reads yesterday's export, not lane 1's   (the whole script)
```

- A trailing comment applies to its own line. A comment on a line of its own
  applies to the next line that isn't blank or another disable comment (which
  may be a header line, e.g. `# After:`).
- `-file` applies to the whole script; it's the only way to silence a finding
  with no line (e.g. no `summary`).
- Several codes: `# swim:lint-ignore bash4,destructive <reason>`.
- The reason is required. Without one (or without a code) the comment is an
  `error` and disables nothing; an unknown code is a `warn`.

## `swim doctor` checks

| check | code | level |
|---|---|---|
| this binary, `bin/swim` at the repo root, and every `swim` on PATH match the host's OS and architecture (read from ELF/Mach-O headers; a darwin/amd64 binary on Apple silicon is `info`: Rosetta) | `binary-platform` | error |
| no `swim` on PATH | `path-missing` | warn |
| more than one distinct `swim` on PATH: listed in order, which runs and which are hidden | `path-multiple` | warn |
| `$SWIM_BIN`, or (unset) the first `swim` on PATH, is a different file from the one running doctor | `swim-bin` | warn |
| the config file doesn't parse or validate | `config-parse` | error |
| the config has no section for this repo | `config-repo` | warn |
| lane scripts exist beyond `lanes` | `lanes-beyond` | warn |
| swim's `.gitignore` block is missing or out of date with `internal/assets/gitignore.txt` | `gitignore` | warn (fixable) |
| lane scripts, `.lane*.rc` or `.swim/` tracked by git (`git ls-files`, read-only) | `tracked` | warn |
| stale `.swim/laneN.pid` files with no process | `stale-pid` | warn (fixable) |
| the config `toolchain` fails in `bash -c` (30 s limit) | `toolchain` | error |
| git missing, or not a git repo | `git` | error |
| `header_env` lists a secret (decision 7 of [secret-masking.md](secret-masking.md)) | `header-env-secret` | warn |
| `.swim.lock` missing (`info`), invalid, or pinning another breaking version (`warn`) | `lock` | info / warn |

`swim doctor --fix` applies only safe local fixes, then runs the checks again:

- rewrite swim's `.gitignore` block (the same merge as `swim init`)
- remove stale pid files (re-checked just before removal)

It never edits config outside the repo, never touches lane scripts, and never
runs git writes.

## Design

- **Commands:** `lint` and `doctor` in `internal/cli`. Findings are a shared
  type, `{Level, File, Line, Code, Message, Fix}`, rendered as text or YAML.
- **Lint:** builds on `internal/syntax` and `internal/lane` (header parsing),
  plus `launcher.ResolveDeps` for the graph checks. Each check is a function,
  `func(script) []Finding`, registered in a table, so adding one is a single
  change. The bash 4 scan skips comments and quoted strings.
- **Doctor:**
  - the OS and architecture check reads ELF/Mach-O headers with `debug/elf`
    and `debug/macho`; nothing is executed
  - PATH shadowing walks `$PATH` the way `exec.LookPath` does
- **Running automatically:** `swim run` runs lint's error-level checks as a
  pre-flight. Most already exist there, so this mostly consolidates.
  `swim new` could suggest `swim lint N` after writing a round.

## Decisions

1. **`swim run` refuses on errors only**, never on warnings; `--strict` on
   `swim lint` / `swim doctor` makes warnings fail too, for CI.
2. **The cross-lane read/write heuristic ships as `info`**, which never
   changes the exit code. Promote it if it proves useful.
3. **Disable comments exist and need a reason**: `# swim:lint-ignore <code>
   <reason>` (this line if trailing, else the next line) and
   `# swim:lint-ignore-file <code> <reason>`. A reasonless comment is an error
   and disables nothing.
4. **The `.swim.log` check is dropped.** The spec had "`.swim.log` is
   git-ignored (it's meant to be committed)", but a later product decision
   git-ignores `.swim.log` by default (it's in swim's `.gitignore` block), so
   doctor doesn't warn about it. Since `swim init --help` invites dropping
   `.swim.log` from the block to commit the project log, a block without it
   still counts as current (`--fix`, when the block is stale for another
   reason, writes the standard block back).
5. **The run pre-flight checks only what the launcher doesn't already
   enforce**: `set-e` and `lane-init` errors, for the lanes about to run
   (pending, not already passed unless `--rerun` or named). It prints the
   findings and exits 2 before anything starts. Blocked commands, `After:`
   and cycles stay with the launcher, so nothing is reported twice. An
   unknown stage is left to its runtime STOP, which existing behaviour and
   tests rely on. It's a separate call in `runLanes` (`lintPreflight` in
   `internal/cli/lint.go`).
6. **Levels**: a missing or wrong `lane_init` is an error (the round would
   claim another lane's log or none); `summary` problems are warnings (the
   exit trap still records a final state); an unknown stage is an error (the
   round stops there), order problems are warnings (the library records WARN).
7. **Doctor additions**: `.swim.lock` (missing is `info`, mismatched or
   invalid is `warn`), `header_env` secrets, git-tracked lane scripts, and
   `path-missing`. `--fix` re-runs the checks and reports what's left.
8. **Output**: text is `file:line  level  message  [code]` with the fix on
   the next line, then `N errors, N warnings[, N infos]`. `--yaml` prints one
   document: `schema` (`swim.lint/v1` or `swim.doctor/v1`), counts and a
   `findings` list of `{level, file, line, code, message, fix}`.
9. **`swim lint` is read-only**: it resolves the repo without migrating logs
   or lane scripts (a syntax 1 script is reported as `[syntax]`, not
   rewritten). `swim new` ends with `then check it: swim lint N`.

## Testing

- **Unit** (`internal/lint`): a clean fixture script produces no findings;
  one fixture per check produces exactly its finding (code, level, line);
  disable comments are honoured (same line, next line, file, several codes)
  and rejected without a reason or with an unknown code; the comment/quote/
  heredoc scanner. (`internal/doctor`): `Platform` on tiny Go programs
  cross-compiled at test time for linux/amd64, linux/arm64, darwin/amd64 and
  darwin/arm64, plus a script and junk; a wrong-platform `swim` first on
  PATH; the `.gitignore` block; stale pid files and `Fix`; `header_env`
  secrets; the toolchain; the lock; lanes beyond.
- **Go e2e** (`internal/e2e/lint_test.go`): `swim lint` exit codes,
  `--strict`, `--yaml`, named lanes, read-only on a syntax 1 script; the run
  pre-flight (exit 2, nothing runs, disable comment with and without a
  reason); `swim doctor` in a scratch repo with a stale `.gitignore` block, a
  stale pid file, a git-tracked lane script and a cross-compiled
  wrong-platform `swim` first on PATH; `--fix` repairs only the block and the
  pid file (config, lane scripts, the binary and the git index untouched); a
  broken config, a repo without a section, a `header_env` secret and a
  failing toolchain.
- **Scenarios:** `dag99` (and the scenarios built on it, plus `dag99-locks`)
  runs `swim lint --strict` over its 99 generated lanes during setup; they
  lint clean. The `blocked` scenario's lanes deliberately contain blocked
  commands and `flaky`'s aren't checked in setup.
