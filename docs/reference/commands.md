# Commands

`swim <command> --help` has each command's detail. A command that takes a
lane takes its number, a job id (8+ characters), a task name or the
script's path; several: `2 3` or `2,3`. `swim N ...` is `swim run N ...`.
`swim` alone, in a repo, says what needs you.

| | |
|---|---|
| **Lanes** | |
| `init` | Set up this repo: config entry, `.swim.lock`, `.gitignore` block, `.swim/status.yml`. |
| `new N\|TASK "<goal>"` | Write `lane.N.sh` from the template with a new job id; a TASK takes the lowest free lane. |
| `lint [N ...]` | Check lane scripts; changes nothing. |
| `claim N\|RESOURCE` | Hold a lane while a session writes it, or a shared resource. |
| `archive N ... [--as NAME]` / `stub N "<msg>"` / `clean` | Keep a finished log; free the lane; both, for every finished lane. |
| `note [--lane N] "<text>"` | A decision or finding in `.swim.log`. |
| **Rounds** | |
| `run [N ...]` / `all [--rerun] [--fresh]` | Run lanes / every pending round that hasn't passed. |
| `queue N ...` / `watch` / `wait` | Queue a batch; the swimmer; block until done. See [queue](queue.md). |
| `interrupt N` | Stop one lane at its next step. |
| `ci [N ...] [--changed]` | Run rounds in CI. See [CI](ci.md). |
| `checkpoint [ls\|rm]` | The steps the next run keeps. |
| **Guards** | |
| `approve` / `revoke` | Signed approvals. See [guards](guards.md). |
| `keys` | Who's who: `lookup.yml`, `trust.yml` and its pin. |
| **Logs and the record** | |
| `status [N]` | Every lane's state; each pending lane's dependencies and guards. |
| `log [N\|JOB\|RUN]` | A lane's log, or the project log. See [logs](logs.md). |
| `report [--earmark]` | What changed since you last looked. |
| `verify` / `history` / `chain` / `store` / `signoff verify` | The [chain of custody](chain.md). |
| **Environment** | |
| `config [edit \| KEY [VALUE]]` | Settings on one screen; see [config](config.md). |
| `env [N]` | The env a lane gets, masked, with each key's source. |
| `doctor [--fix]` | Check the environment. |
| `lock [--upgrade]` / `version` / `changelog` | Syntax versions; the latest release; what changed. |
| `skill` | How to install the Claude skill. |
| `guide [TOPIC]` | This reference. |
| **In a lane script** (any language) | |
| `run`, `gate`, `snapshot`, `guard`, `decide`, `stage`, `drift`, `stop`, `msg`, `input`, `output` | Steps and marks. See [lanes](lanes.md) and [other languages](other-languages.md). |
| `step -- cmd` / `lib` | Run and log one command (what `run` calls); print the bash library. |
