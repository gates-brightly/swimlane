# Commands

`swim <command> --help` has each command's detail. A command that takes a
lane takes its number, a round id (8+ characters), a task name or the
script's path; several: `2 3` or `2,3`. `swim N ...` is `swim run N ...`.
`swim` alone, in a repo, says what needs you.

| | |
|---|---|
| **Lanes** | |
| `init` | Set up this repo: your config if missing, the `.gitignore` block, `.swim/status.yml`. |
| `new N\|TASK "<goal>"` | Write `lane.N.sh` from the template with a new round id; a TASK takes the lowest free lane. |
| `lint [N ...]` | Check lane scripts; changes nothing. |
| `archive N ... ["<note>"] [--as NAME]` / `archive --passed` | Keep an ended round's log and empty the lane (its script is removed; a committed lane isn't archived); `--passed`: every lane whose round passed. |
| `helpers [MODULE]` / `helpers add MODULE...` | The lane helper modules swim ships (one line each), a module's functions, and the copies in this repo; write a module to `lib/swim/` (again: update it). See [lanes](lanes.md#shared-helpers). |
| `note [--lane N] "<text>"` | A decision or finding in `.swim.log`. |
| **Rounds** | |
| `run [N ...]` / `all [--rerun]` | Run lanes / every ready round that hasn't passed. |
| `queue N ...` / `watch` / `wait` | Queue a batch; the watcher; block until work ends. See [queue](queue.md). |
| `interrupt N` | Stop one lane at its next step. |
| `ci [N ...] [--changed]` | Run rounds in CI. See [CI](ci.md). |
| **Logs and the record** | |
| `status [N]` | Every lane's state; each ready lane's dependencies and guards. |
| `log [N\|ROUND-ID\|RUN]` | A lane's log, or the project log (`--since 1h\|RUN\|ROUND-ID`: what happened since). See [logs](logs.md). |
| `verify` / `chain` / `keys` | The [chain of custody](chain.md), and the key this machine signs it with. |
| **Environment** | |
| `config [edit \| KEY [VALUE]]` | Settings on one screen; see [config](config.md). |
| `env [N]` | The env a lane gets, masked, with each key's source. |
| `doctor [--fix]` | Check the environment. |
| `version` / `changelog` | The latest release, and the syntax version; what changed. |
| `skill` | How to install the Claude skill. |
| `guide [TOPIC]` | This reference. |
| **In a lane script** (any language) | |
| `step`, `gate`, `snapshot`, `guard`, `stage` | Steps and marks. See [lanes](lanes.md) and [other languages](other-languages.md). |
