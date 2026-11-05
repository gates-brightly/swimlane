# Concepts

Four ideas carry swim:

| | |
|---|---|
| **lane** | A numbered slot, 1..99: one script (`lane.N.sh`, or [another language](other-languages.md)) and one log. See [lanes](lanes.md). |
| **round** | One go at a lane's work: what its script holds now, one goal ("cut over orders-api"). Disposable. |
| **guard** | A named approval for one destructive step (`FIN_ALLOW_DELETE`). Unapproved, the step is a dry run. See [guards](guards.md). |
| **log** | What happened, step by step (`swim log N`), and the project log, one line per action (`swim log`). See [logs](logs.md). |

What a lane script is made of:

| | |
|---|---|
| **step** | One command run through `step`, `gate` or `snapshot`, logged with its command, output, exit code and time. A failed step says why in one line. |
| **gate** | A step whose failure stops the round. |
| **snapshot** | A read-only step whose output is also saved under `.swim/snapshots/`. |
| **stage** | Where a round is: snapshot, check, change, verify. See [lanes](lanes.md#stages). |

Running rounds:

| | |
|---|---|
| **run** | One `swim run` or `swim all`, with an id (`r-<utc>-<hex>`, `$SWIM_RUN`): `swim log <run>` shows it. |
| **batch** | Lanes one `swim queue` call queued, pinned to their scripts and approvals. See [queue](queue.md). |
| **watcher** | `swim watch`: runs queued batches as their lanes come free. |

When you need them:

| | |
|---|---|
| **round id** | `# Round-ID:`, written by `swim new` (`$SWIM_ROUND_ID`). Naming it instead of a lane (`swim run 3f2a9c1e`) pins that exact round. |
| **task** | `# Task: renew-certs`: a name any command takes in place of the lane number. |
| **dependency / lock** | `# After: 2 3` runs a lane after those pass; `# Locks: orders-db` keeps lanes sharing one from overlapping. See [running](running.md). |
| **manifest** | What swim pins before a round starts, and runs from a read-only copy. See [manifest](manifest.md). |
| **chain** | Every round and approval, hash-linked and signed, checked by `swim verify`. See [chain](chain.md). |
| **signoff** | `# Signoff:` posts a passed round as a signed commit status. See [CI](ci.md#local-ci). |
| **archive** | `swim archive N "<note>"` keeps the round's log (`laneN.prev-<name>.log`) and empties the lane. |

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
