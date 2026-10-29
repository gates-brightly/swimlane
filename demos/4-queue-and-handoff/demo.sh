#!/usr/bin/env bash
# Demo 4: nobody waits on anybody. The operator approves a batch and goes
# home; a worker runs it when swim is free; the AI catches up from files,
# not from a chat transcript. A batch is pinned to the scripts it was
# approved with.
. "$(dirname "$0")/../_lib/common.sh"
demo_init 4-queue-and-handoff

fakecloud deploy api 2.4.0 0 >/dev/null
fakecloud seed-table sessions "s1,active" "s2,stale" "s3,stale" "s4,active" "s5,stale"
demo_lanes
demo_commit "the AI's rounds for tonight"

scene "1. The AI planned tonight's work"
say "Two rounds: roll api forward, then clear the sessions the bug leaked." \
    "lane.2 is Python. It calls swim's steps as commands: same stages, guards and log."
show 'swim status'
pause

scene "2. The operator approves the batch and leaves"
say "One decision, up front, for the whole batch. Then nobody has to be at a keyboard."
show 'swim queue add 1 2 --approve DEPLOY_ALLOW_API,SESSIONS_ALLOW_PURGE --note "2.4.1 rollout + session cleanup"'
show 'swim queue'
pause

scene "3. A worker runs it"
say "In real life: swim watch in a tmux pane, on a box with the prod credentials." \
    "It runs each batch as soon as its lanes are free."
show 'swim watch --once --no-tui'
show 'fakecloud ls'
pause

scene "4. The AI catches up from files, not chat"
say "Next morning, a fresh AI session (or a teammate) needs no hand-over:"
show 'swim log'
pause
show 'swim status --yaml | head -30'
pause
show 'swim log 2 --yaml | sed -n "/^results:/,\$p" | head -30'
say "Structured, versioned (swim.log/v1), and small: the AI reads results without" \
    "anyone pasting terminal output into a chat."
pause

scene "5. A queued batch runs what was approved, nothing else"
say "The operator queues tomorrow's rerun. Then the AI 'improves' the script:"
fakecloud seed-table sessions "s6,active" "s7,stale"
show 'swim queue add 2 --approve SESSIONS_ALLOW_PURGE --note "nightly session cleanup"'
show "sed -i.orig '/delete/s/\"sessions\", \"stale\"/\"sessions\", \"s\"/' lane.2.py && rm lane.2.py.orig && grep -n 'fakecloud\", \"delete' lane.2.py"
say "That would delete every session. The worker:"
show 'swim watch --once --no-tui 2>&1 | tail -5'
show 'fakecloud select sessions'
say "Skipped: lane.2.py changed since it was queued. Nothing deleted."
demo_done
