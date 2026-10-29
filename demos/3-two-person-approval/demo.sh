#!/usr/bin/env bash
# Demo 3: two-person approval. lookup.yml says a FIN_ guard needs two
# signatures from the dba group. Teammates sign the exact script from their
# own machines with their SSH keys; an edit afterwards voids the approval.
. "$(dirname "$0")/../_lib/common.sh"
demo_init 3-two-person-approval

fakecloud seed-table orders \
  "1001,2019-03-02,49.00" "1002,2019-11-20,12.50" \
  "2001,2024-06-01,80.00" "2002,2025-01-15,23.99" "2003,2026-09-30,5.00"
for p in alice bob carol; do demo_person $p; done
cat > lookup.yml <<YML
# Who's who, and what each guard needs. Committed with the repo.
users:
  operator: {keys: ["$(pubkey operator)"]}
  alice:    {keys: ["$(pubkey alice)"]}
  bob:      {keys: ["$(pubkey bob)"]}
  carol:    {keys: ["$(pubkey carol)"]}
groups:
  dba: [alice, bob]
require:
  - guards: [FIN_ALLOW_*]
    group: dba
    count: 2          # two different people from dba
    signed: true      # only signatures meet it (a prompt or FLAG=1 doesn't)
YML
demo_lanes 1
demo_commit "lookup.yml and the AI's round"

scene "1. The rule"
say "Deleting finance data needs two DBAs. That's written down, in the repo:"
show 'sed -n "/^groups:/,\$p" lookup.yml'
show 'swim keys'
pause
show 'sed -n "/^stage change/,/^fi/p" lane.1.sh'
pause

scene "2. The operator alone gets a dry run"
show 'swim run 1 --plain --no-prompt'
show 'fakecloud rows orders'
pause

scene "3. Teammates approve, from their own machines"
say "alice signs. swim shows exactly what she's approving: the script, how it" \
    "runs, the policy in force. One signature isn't enough yet:"
show 'as alice swim approve 1 --guards FIN_ALLOW_DELETE'
pause
say "carol signs too, but she isn't a DBA:"
show 'as carol swim approve 1 --guards FIN_ALLOW_DELETE'
pause
say "bob is the second DBA:"
show 'as bob swim approve 1 --guards FIN_ALLOW_DELETE'
show 'ls approvals/'
say "Approvals are plain files under approvals/, committed like code."
demo_commit "approvals from alice, carol and bob"
pause

scene "4. Now it runs, with nobody at the keyboard"
show 'swim run 1 --plain --no-prompt'
show 'fakecloud select orders'
pause

scene "5. An approval is for these exact bytes"
say "Put the 2019 rows back, then have the AI 'tweak' the script after approval:"
fakecloud seed-table orders \
  "1001,2019-03-02,49.00" "1002,2019-11-20,12.50" \
  "2001,2024-06-01,80.00" "2002,2025-01-15,23.99" "2003,2026-09-30,5.00"
cp lane.1.sh "$DEMO_DIR/lane.1.approved"
show "sed -i.orig 's/,2019-/,20/g' lane.1.sh && rm lane.1.sh.orig && grep -n 'fakecloud delete' lane.1.sh"
say "That now deletes every order since 2000. The approvals no longer match:"
show 'swim approve'
show 'swim run 1 --plain --no-prompt'
show 'fakecloud rows orders'
say "Dry run. Nothing was deleted."
cp "$DEMO_DIR/lane.1.approved" lane.1.sh   # back to the approved script
pause

scene "6. Break glass, and it shows"
say "The script is back to what was approved. Then alice withdraws her approval:"
show 'as alice swim revoke 1'
show 'swim approve'
say "It's urgent and alice can't be reached. The operator holds the credentials, so they" \
    "can always override with FLAG=1. swim doesn't pretend otherwise: it records" \
    "it, and verify calls it out."
show 'FIN_ALLOW_DELETE=1 swim run 1 --plain'
show 'fakecloud select orders'
show 'swim history --oneline'
show 'swim verify --levels'
say "authorized: no. The override is in the signed record for anyone who audits it."
demo_done
