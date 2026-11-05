# Concepts

Four ideas carry swim:

| | |
|---|---|
| **lane** | A numbered slot, swim 1..99: one script (`lane.N.sh`, or [another language](other-languages.md)), one log (`.swim/logs/agentN.log`), one worker at a time. `# Task: renew-certs` names it; every command that takes N takes the name, a job id or the script's path too. A repo's lanes are the ones in use (a script that isn't a stub, a current log, a running lane): `swim new N` starts one, `swim archive N` drops one. |
| **round** | What a lane script holds: one goal ("cut over orders-api"). Disposable. A round that passed is done: `swim all` won't run it again (`swim run N` and `swim all --rerun` will). |
| **guard** | A named approval for one destructive step (`FIN_ALLOW_DELETE`). Unapproved, the step is a dry run. See [guards](guards.md). |
| **log** | What happened, step by step (`swim log N`), and the project log, one line per action (`swim log`). See [logs](logs.md). |

Built from them:

| | |
|---|---|
| **step** | One command run through `step`, `gate` or `snapshot`: each records its command, output, exit code and time in the log; `snapshot` also saves the output under `.swim/snapshots/`. |
| **stage** | Where a round is: snapshot, check, change, verify. See [lanes](lanes.md#stages). |
| **gate** | A step whose failure stops the round before any later step. |
| **job** | A round's unique id (`# Job:`, written by `swim new`), exported as `$SWIM_JOB`. Naming a job instead of a lane (`swim run 3f2a9c1e`, `# After: 3f2a9c1e`) pins that exact round; a lane rewritten since is refused. A script without one gets an id when it starts. |
| **run** | One `swim run` or `swim all`, with an id (`r-<utc>-<hex>`) exported as `$SWIM_RUN`, and its lanes as `$SWIM_RUN_LANES`. `swim log <run>` and `swim status --run <run>` show one. |
| **dependency** | `# After: 2 3`: start after those pass; skipped if one fails or is skipped. See [running](running.md). |
| **lock** | `# Locks: orders-db`: lanes sharing one never overlap, in either order. See [running](running.md#locks). |
| **batch** | The lanes one `swim queue` call queued, pinned to their scripts. See [queue](queue.md). |
| **swimmer** | A machine running the queue (`swim watch`). Not a worker: a worker is an AI subagent writing a lane. |
| **manifest** | What swim pins before a round starts. See [manifest](manifest.md). |
| **chain** | Every round and approval, hash-linked and signed: `.swim/chain.jsonl`. See [chain](chain.md). |
| **claim** | A lane held while a session writes it: `swim new` takes one, the round starting or `swim archive` ends it, and another session's `swim new` and `archive` refuse the lane until then (`--force` takes it over). Advisory; `swim status` shows only other sessions' claims. |
| **signoff** | `# Signoff:` posts a round's result as a signed commit status. See [CI](ci.md#local-ci). |
| **archive / stub** | `swim archive N "<note>"` renames a finished log `agentN.prev-<what>.log` and leaves a stub, the placeholder a lane holds between rounds, carrying the note. Logs are kept; rounds are not. |
