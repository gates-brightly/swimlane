# Commands

`swim <command> --help` has each command's detail. A command that takes a
lane takes its number, a job id (8+ characters), a task name or the
script's path; several: `2 3` or `2,3`. `swim N ...` is `swim run N ...`.
`swim` alone, in a repo, says what needs you.

| | |
|---|---|
| **Lanes** | |
| `init` | Set up this repo: config entry, `.gitignore` block, `.swim/status.yml`. |
| `new N\|TASK "<goal>"` | Write `lane.N.sh` from the template with a new job id; a TASK takes the lowest free lane. |
| `lint [N ...]` | Check lane scripts; changes nothing. |
| `archive N ... ["<note>"] [--as NAME]` / `archive --done` | Keep a finished round's log and free the lane (its script becomes a stub with the note); `--done`: every lane whose round passed. |
| `note [--lane N] "<text>"` | A decision or finding in `.swim.log`. |
| **Rounds** | |
| `run [N ...]` / `all [--rerun]` | Run lanes / every pending round that hasn't passed. |
| `queue N ...` / `watch` / `wait` | Queue a batch; the swimmer; block until done. See [queue](queue.md). |
| `interrupt N` | Stop one lane at its next step. |
| `ci [N ...] [--changed]` | Run rounds in CI. See [CI](ci.md). |
| **Guards** | |
| `keys` | The key this machine signs with. |
| **Logs and the record** | |
| `status [N]` | Every lane's state; each pending lane's dependencies and guards. |
| `log [N\|JOB\|RUN]` | A lane's log, or the project log (`--since 1h\|RUN\|JOB`: what happened since). See [logs](logs.md). |
| `verify` / `chain` / `signoff verify` | The [chain of custody](chain.md). |
| **Environment** | |
| `config [edit \| KEY [VALUE]]` | Settings on one screen; see [config](config.md). |
| `env [N]` | The env a lane gets, masked, with each key's source. |
| `doctor [--fix]` | Check the environment. |
| `version` / `changelog` | The latest release, and the syntax version; what changed. |
| `skill` | How to install the Claude skill. |
| `guide [TOPIC]` | This reference. |
| **In a lane script** (any language) | |
| `run`, `gate`, `snapshot`, `guard`, `stage` | Steps and marks. See [lanes](lanes.md) and [other languages](other-languages.md). |
