#!/usr/bin/env bash
# Demo 4: a record you can believe. Every round and approval is signed and
# hash-linked into .swim/chain.jsonl. swim verify says how far it can be
# believed, and catches a log edited after the fact.
. "$(dirname "$0")/../_lib/common.sh"
demo_init 4-tamper-evidence

for s in api worker; do fakecloud deploy $s 2.4.0 0 >/dev/null; done
demo_lanes
demo_commit "the AI's rounds"

scene "1. A night's work, on the record"
fakecloud break worker >/dev/null
show 'swim all --plain 2>&1 | sed -n "/^swim summary/,\$p"'
say "The worker change failed. That's what happened, and it's now on the record."
pause
show 'swim log --since 1h'
show 'swim verify'
say "Every link and signature matches, and every round's log is what was recorded."
pause

cp -R .swim "$DEMO_DIR/swim.backup"
restore() { rm -rf .swim && cp -R "$DEMO_DIR/swim.backup" .swim; }

scene "2. Tamper: rewrite a log to hide the failure"
show 'grep -nE "FAIL|END" .swim/logs/lane2.log'
show "sed -i.orig -e '/failed: FAIL/d' -e 's/FAIL  set pool_size=50 (exit 1)/PASS  set pool_size=50         /' -e 's/END FAIL  pass=3 fail=1/END PASS  pass=4 fail=0/' -e 's/change FAIL/change PASS/' .swim/logs/lane2.log && rm .swim/logs/lane2.log.orig && grep -nE 'pool_size|END' .swim/logs/lane2.log"
show 'swim verify'
say "Each round's log section is hashed into its signed chain entry."
restore
pause

scene "3. Hand the record to an auditor"
say "The chain is plain JSON lines. Signatures are ssh-keygen -Y signatures, the same" \
    "format as git's SSH commit signing, so it can be checked without swim:"
show 'head -1 .swim/chain.jsonl | cut -c1-300; echo ...'
demo_done
