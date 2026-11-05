# Concepts

Four ideas carry swim:

| | |
|---|---|
| **lane** | A numbered slot, lane 1..99: one script (`lane.N.sh`, or [another language](other-languages.md)), one log (`.swim/logs/laneN.log`), one worker at a time. `# Task: renew-certs` names it; every command that takes N takes the name, a round id or the script's path too. A repo's lanes are the ones in use (a script that isn't a stub, a current log, a running lane): `swim new N` starts one, `swim archive N` drops one. |
| **round** | What a lane script holds: one goal ("cut over orders-api"). Disposable. A round that passed stays passed: `swim all` won't run it again (`swim run N` and `swim all --rerun` will). |
| **guard** | A named approval for one destructive step (`FIN_ALLOW_DELETE`). Unapproved, the step is a dry run. See [guards](guards.md). |
| **log** | What happened, step by step (`swim log N`), and the project log, one line per action (`swim log`). See [logs](logs.md). |

Built from them:

| | |
|---|---|
| **step** | One command run through `step`, `gate` or `snapshot`: each records its command, output, exit code and time in the log; `snapshot` also saves the output under `.swim/snapshots/`. |
| **stage** | Where a round is: snapshot, check, change, verify. See [lanes](lanes.md#stages). |
| **gate** | A step whose failure stops the round before any later step. |
| **round id** | A round's unique id (`# Round-ID:`, written by `swim new`), exported as `$SWIM_ROUND_ID`. Naming a round id instead of a lane (`swim run 3f2a9c1e`, `# After: 3f2a9c1e`) pins that exact round; a lane rewritten since is refused. A script without one gets an id when it starts. |
| **run** | One `swim run` or `swim all`, with an id (`r-<utc>-<hex>`) exported as `$SWIM_RUN`, and its lanes as `$SWIM_RUN_LANES`. `swim log <run>` and `swim status --run <run>` show one. |
| **dependency** | `# After: 2 3`: start after those pass; skipped if one fails or is skipped. See [running](running.md). |
| **lock** | `# Locks: orders-db`: lanes sharing one never overlap, in either order. See [running](running.md#locks). |
| **batch** | The lanes one `swim queue` call queued, pinned to their scripts. See [queue](queue.md). |
| **watcher** | A machine running the queue (`swim watch`). Not a worker: a worker is an AI subagent writing a lane. |
| **manifest** | What swim pins before a round starts. See [manifest](manifest.md). |
| **chain** | Every round and approval, hash-linked and signed: `.swim/chain.jsonl`. See [chain](chain.md). |
| **claim** | A lane held while a session writes it: `swim new` takes one, the round starting or `swim archive` ends it, and another session's `swim new` and `archive` refuse the lane until then (`--force` takes it over). Advisory; `swim status` shows only other sessions' claims. |
| **signoff** | `# Signoff:` posts a round's result as a signed commit status. See [CI](ci.md#local-ci). |
| **archive / stub** | `swim archive N "<note>"` renames an ended round's log `laneN.prev-<what>.log` and leaves a stub, the placeholder script carrying the note: the lane is empty. Logs are kept; rounds are not. |

## Lane states

Every view (bare swim, swim status, swim watch, a run's panel and summary, --yaml, notifications) names a lane's state with one of these words:

| | |
|---|---|
| **ready** | A round is written and hasn't run. |
| **queued** | Its batch is queued (`swim queue`), waiting for a watcher. |
| **waiting** | Held up, always with why: `waiting on lane 1, 3`, `waiting for lock orders-db`, `waiting for a slot (#2)`, `waiting for approval: ...`. |
| **running** | Its round is running. |
| **passed** | Its round passed. |
| **failed** | Its round failed (`failed exit 1`). |
| **skipped** | Its round was skipped (a lane it runs after didn't pass) or dry-ran a guard. |
| **interrupted** | Its round was stopped (Ctrl-C, `swim interrupt`), or its process is gone. |
| **empty** | No round in the slot: no script, a stub, or no `# Round:` line. |

The uppercase words in a lane log (`PASS`, `FAIL`, `SKIP`, `STOP`, `BLOCKED`, `== END PASS`) mark steps and rounds there; they are not lane states.
