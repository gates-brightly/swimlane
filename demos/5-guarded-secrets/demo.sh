#!/usr/bin/env bash
# Demo 5: guarded secrets. A guard is normally advisory: a script could
# just not call it. For a credential that lives only in swim's vault, it's
# enforced: swim releases the secret only after the lane's guard is approved
# in this run. Released values are masked in every log.
. "$(dirname "$0")/../_lib/common.sh"
demo_init 5-guarded-secrets

fakecloud seed-table orders_2019 "1001,2019-03-02,49.00" "1002,2019-11-20,12.50"
fakecloud seed-table orders "2001,2024-06-01,80.00"
fakecloud set-admin-pw 'hunter2-prod-admin'
cat > swim.yml <<'YML'
# The admin password is released only to a lane whose DB_ALLOW_DROP guard
# was approved in this run.
secrets:
  DB_ADMIN_PW: { guard: DB_ALLOW_DROP }
YML
demo_lanes
demo_commit "swim.yml and the AI's rounds"

scene "1. The credential lives in swim's vault, not in the AI's reach"
say "The operator stores the DB admin password, encrypted to their SSH key:"
show "echo 'hunter2-prod-admin' | swim secret set DB_ADMIN_PW"
show 'swim secret ls'
show 'cat swim.yml'
say "(In real life, protect that key with a passphrase; swim warns when it has none.)"
pause

scene "2. A script that skips its guard gets nothing"
show 'sed -n "/^stage change/,\$p" lane.2.sh'
show 'swim run 2 --plain --no-prompt 2>&1 | grep -v "has no passphrase"'
show 'fakecloud ls'
say "Refused, and the table is still there. The refusal is on the record:"
show 'grep secret-refused .swim.log'
pause

scene "3. The right script, not approved: still nothing"
show 'sed -n "/^stage change/,/^fi/p" lane.1.sh'
show 'swim run 1 --plain --no-prompt 2>&1 | grep -E "SKIP|refused|summary:"'
pause

scene "4. Approved: released, used, and masked"
show 'DB_ALLOW_DROP=1 swim run 1 --plain 2>&1 | grep -v "has no passphrase"'
say "The debug step printed the connection string, and swim masked the password:" \
    "on screen, in the log, in the hub. Never in any log:"
show 'grep -rc hunter2 .swim/ .swim.log | grep -v ":0" || echo "hunter2 appears nowhere under .swim/ or in .swim.log"'
pause
show 'grep -E "secret" .swim.log'
show 'swim history --oneline | grep -i secret'
say "Which lane got which secret, under which approval. Never the value."
demo_done
