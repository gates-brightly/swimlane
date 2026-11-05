# Demos

Four short, self-contained walkthroughs for showing swim to a team. Each
one builds swim from this checkout, creates a scratch repo with its own
HOME and SSH key, and changes a pretend cloud (`_lib/bin/fakecloud`).
You don't need cloud credentials, and nothing touches your `~/.ssh`,
`~/.config/swim` or this repo's `.swim`.

```sh
demos/1-guarded-change/demo.sh          # press Enter between scenes
DEMO_AUTO=1 demos/2-parallel-dag/demo.sh   # no pauses (a smoke test)
SWIM_BIN=$(which swim) demos/3-queue-and-handoff/demo.sh   # skip the build
```

Each demo prints the commands it runs as `$ ...`, with a line of narration
before or after. The scratch repo is kept afterwards in
`${TMPDIR:-/tmp}/swim-demos/<demo>/repo` for questions ("what's in
`.swim/`?"), and the next run of that demo wipes it.

| Demo | Shows | Time |
|---|---|---|
| [1-guarded-change](1-guarded-change/demo.sh) | The core loop: the AI writes, a person approves, the log reports | 4 min |
| [2-parallel-dag](2-parallel-dag/demo.sh) | A release as a DAG: parallel lanes, a lock, failure, then resuming | 4 min |
| [3-queue-and-handoff](3-queue-and-handoff/demo.sh) | Approve a batch, a worker runs it, the AI catches up from files | 4 min |
| [4-tamper-evidence](4-tamper-evidence/demo.sh) | `swim verify` catching an edited log | 3 min |

For a 15-minute slot: **1**, then **3**, then **4**. That's the story of
"the AI can't act alone, a person decides, and you can prove it
afterwards". Bring in **2** for teams with big migrations.

## Talking points

### 1. Guarded change

- The AI never had prod access. It wrote `lane.1.sh`, and a person ran it.
- Read-only stages come first (`snapshot`, `check`). The only destructive
  step sits behind `guard`.
- With no one to approve, the guarded step is a **dry run**. `verify` then
  fails honestly instead of claiming success.
- At a terminal, the question comes from swim, not from the script.
- Scene 4: a script that sets its own approval flag gets a dry run. The
  flag counts only when it comes from the environment of whoever started
  `swim run`.
- `swim log`, `swim status` and `swim history` are what the AI reads back
  next. Nobody pastes terminal output into a chat.

### 2. Parallel DAG

- `# After:` and `# Locks:` in a script's header are all the orchestration
  it needs.
- api and worker share the `db-migrations` lock, so they take turns. web
  doesn't wait for either of them.
- When worker fails, smoke test and announce are **skipped**, not run
  against a half-finished release.
- After the fix, only what's left runs. Build, api and web aren't redone.

### 3. Queue and hand-off

- One approval up front for a whole batch, then nobody needs to be at a
  keyboard. `swim watch` would normally run in tmux or under systemd.
- lane.2 is Python. There's no SDK: it calls swim's steps as commands
  (`swim run "label" -- cmd`, `swim guard FLAG "action"`), so it gets the
  same stages, guards and log. Lanes can be written in any language.
- `swim log` (the project log), `swim status --yaml` and
  `swim log N --yaml` are what a fresh AI session reads to catch up.
  They're versioned, structured and small.
- A queued batch is pinned to its scripts. An edit after queueing means
  the lane is **skipped**, not run.

### 4. Tamper evidence

- `.swim/chain.jsonl` holds every round and approval. Each entry is
  SSH-signed and linked to the hash of the one before.
- Edit a log to hide a failure: verify says `NOT VERIFIED` and names the
  entry.
- The signatures are `ssh-keygen -Y` signatures, so an auditor can check
  the chain without swim.

## Writing another demo

Copy a demo directory. `_lib/common.sh` has the helpers: `demo_init`,
`demo_lanes`, `demo_commit`, `scene`, `say`, `show 'CMD'` and `pause`. Lane scripts go in the
demo's `lanes/` folder; the repo's `.gitignore` makes an exception for
them. Run the demo with `DEMO_AUTO=1` before you present it.

`demo_init` falls back to `/tmp` when `TMPDIR` is long. swim's approval
socket lives under `TMPDIR`, and a socket path over about 100 characters
fails to bind, which changes how guards behave.
