#!/usr/bin/env bash
# Demo 1: a guarded change. The AI writes the script; nothing destructive
# happens until a person says yes, and a script can't say yes for itself.
. "$(dirname "$0")/../_lib/common.sh"
demo_init 1-guarded-change

fakecloud seed-table users \
  "1,ana@acme.com" "2,raj@acme.com" "3,li@acme.com" \
  "4,load-1@test.example" "5,load-2@test.example" "6,load-3@test.example"
demo_lanes 1
demo_commit "the AI's round"

scene "1. The AI wrote a round"
say "An AI session was asked to clean up after a load test. It can't reach prod," \
    "so it wrote lane.1.sh instead: read-only checks, then one guarded change."
show 'sed -n "/^stage snapshot/,\$p" lane.1.sh'
pause
show 'swim lint'
say "swim lint finds nothing to fix, and the guard will ask before it acts."
pause

scene "2. Run it: a dry run until someone approves"
show 'swim run 1 --plain --no-prompt'
say "--no-prompt means no one is there to answer, so the guarded step was SKIPPED." \
    "verify FAILs honestly: the test accounts are still there."
show 'fakecloud select users'
pause

scene "3. A person approves"
if [ -t 0 ] && [ -z "${DEMO_AUTO:-}" ]; then
  say "Now run it at a terminal. swim, not the script, asks the question." \
      "Answer y when it asks."
  show 'swim run 1'
else
  say "(no terminal: approving with the flag instead of the prompt)"
  show 'USERS_ALLOW_PURGE=1 swim run 1 --plain'
fi
show 'fakecloud select users'
pause

scene "4. A script can't approve itself"
demo_lanes 2
say "lane.2.sh sets USERS_ALLOW_PURGE=1 itself before calling guard:"
show 'grep -n -A3 "export USERS_ALLOW_PURGE" lane.2.sh'
show 'swim run 2 --plain --no-prompt'
say "Refused: the flag has to come from the person who started swim run," \
    "not from inside the lane. Nothing was deleted:"
show 'fakecloud rows users'
pause

scene "5. What's left behind"
say "Every step, result and approval is on disk, readable by people and by the AI."
show 'swim status'
show 'swim log 1 | tail -25'
pause
show 'swim history --oneline'
say "The chain records who approved what and how (the prompt, a flag, -y), signed and hash-linked."
show 'swim verify'
demo_done
