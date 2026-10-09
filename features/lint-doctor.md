# swim lint and swim doctor

Status: proposed

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

| check | level |
|---|---|
| `# Round:` missing, or a template placeholder left in | error |
| `# After:` refers to an unknown lane or job, a cycle, or the lane itself | error (`swim run` refuses these today; lint reports them earlier) |
| `set -e` / `set -o errexit` | error |
| no `lane_init`, `lane_init` with the wrong lane number, or `summary` missing or not last | error / warn |
| bash 4 features: `declare -A`, `mapfile`, `readarray`, `${x,,}`, `${x^^}`, `\|&`, `coproc` | warn |
| a guard flag used in `guard` but not listed in the `# Guards:` header, or the other way round | warn |
| a destructive-looking command (`delete`, `destroy`, `rm -rf`, `drop`) not inside a `guard` block | warn |
| a blocked command (see [blocked-commands.md](blocked-commands.md)) | error |
| a variable named in config `secret_env` echoed directly (see [secret-masking.md](secret-masking.md)) | warn |
| **reads another lane's output without depending on it** (heuristic: a path written in lane A, e.g. `> path`, `-o path` or `cp … path`, read in lane B, which doesn't depend on A) | warn |

The last check is a heuristic and can't be complete. It reports what it saw
(`lane.1.sh:44 writes $D/combined.html`) so a person can judge.

## `swim doctor` checks

| check | level |
|---|---|
| this binary's OS and architecture match the host (catches an "exec format error" before it happens, for `bin/swim` and every `swim` on PATH) | error |
| more than one `swim` on PATH: list them in order, and which wins | warn |
| `$SWIM_BIN` or the `swim` that lanes will use is different from the one running doctor | warn |
| the config file parses; this repo has a section; lane scripts exist beyond `lanes` | error / warn |
| swim's `.gitignore` block is out of date with the current patterns, or lane scripts are tracked by git | warn |
| `.swim.log` is git-ignored (it's meant to be committed) | warn |
| stale `.swim/laneN.pid` files with no process | warn (fix: `swim status --rebuild`) |
| toolchain in config fails to load (`nvm use` etc.) | error |
| git missing, or not a git repo | error |

`swim doctor --fix` applies only safe local fixes:

- rewrite swim's `.gitignore` block
- remove stale pid files

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

## Open questions

1. **Should `swim run` refuse to start on lint warnings?** Recommend no, only
   errors, with `--strict` for CI.
2. **Is the cross-lane read/write heuristic worth the false positives?**
   Recommend shipping it as `info` first and promoting it if it proves useful.
3. **Disabling checks:** per-check disable comments
   (`# swim:lint-ignore set-e`)? Recommend yes, but require a reason after
   the code.

## Testing

- **Unit:** one fixture script per check, each producing exactly the expected
  finding, and a clean script producing none; ELF/Mach-O fixtures for the
  architecture check.
- **Go e2e:** `swim lint` exit codes; `swim doctor` in a scratch repo with a
  deliberately stale `.gitignore` block, a stale pid file and a Linux-built
  binary on Mac (or the other way round, using a fixture binary); `--fix`
  repairs only the safe items.
- **Scenarios:** run `swim lint` over every scenario's generated lanes as part
  of setup. The suite's own lanes must lint clean, which also keeps the
  scenarios honest.
