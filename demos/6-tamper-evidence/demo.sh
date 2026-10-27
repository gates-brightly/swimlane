#!/usr/bin/env bash
# Demo 6: a record you can believe. Every round and approval is signed and
# hash-linked into .swim/chain.jsonl. swim verify says how far it can be
# believed, and catches a log edited after the fact, entries removed from
# the end, and a swapped signing key.
. "$(dirname "$0")/../_lib/common.sh"
demo_init 6-tamper-evidence

for s in api worker; do fakecloud deploy $s 2.4.0 0 >/dev/null; done
mkdir -p "$DEMO_DIR/audit-volume"
cat > swim.yml <<YML
# Send a signed checkpoint of the chain's head here at the end of every run.
# In real life: an append-only volume someone else holds.
chain_anchors: ["file:$DEMO_DIR/audit-volume"]
YML
cat > lookup.yml <<YML
users:
  operator: {keys: ["$(pubkey operator)"]}
YML
demo_lanes
swim keys trust >/dev/null 2>&1      # trust.yml: this machine's key signs the chain
swim keys pin >/dev/null 2>&1        # pin it outside the repo
demo_commit "swim.yml, lookup.yml, trust.yml, the AI's rounds"

scene "1. A night's work, on the record"
fakecloud break worker >/dev/null
show 'swim all --plain 2>&1 | sed -n "/^swim summary/,\$p"'
say "The worker change failed. That's what happened, and it's now on the record."
pause
show 'swim history --oneline'
show 'swim verify'
say "consistent: links and signatures match. trusted: signed by keys in trust.yml," \
    "pinned outside the repo. anchored: matches the checkpoint on the audit volume."
pause

cp -R .swim "$DEMO_DIR/swim.backup"
restore() { rm -rf .swim && cp -R "$DEMO_DIR/swim.backup" .swim; }

scene "2. Tamper: rewrite a log to hide the failure"
show 'grep -nE "FAIL|END" .swim/logs/agent2.log'
show "sed -i.orig -e '/failed: FAIL/d' -e 's/FAIL  set pool_size=50 (exit 1)/PASS  set pool_size=50         /' -e 's/END FAIL  pass=3 fail=1/END PASS  pass=4 fail=0/' -e 's/change FAIL/change PASS/' .swim/logs/agent2.log && rm .swim/logs/agent2.log.orig && grep -nE 'pool_size|END' .swim/logs/agent2.log"
show 'swim verify'
say "Each round's log section is hashed into its signed chain entry."
restore
pause

scene "3. Tamper: delete the failed round from the chain"
show 'n=$(grep -n "\"lane\":2" .swim/chain.jsonl | head -1 | cut -d: -f1); head -n $((n - 1)) .swim/chain.jsonl > c && mv c .swim/chain.jsonl; swim history --oneline'
say "What's left is a perfectly consistent, shorter history. But a checkpoint left the machine:"
show 'swim verify'
restore
pause

scene "4. Tamper: sign with a different key"
say "Someone adds their own key to trust.yml so they can re-sign the chain:"
ssh-keygen -q -t ed25519 -N '' -C mallory@elsewhere -f "$DEMO_DIR/mallory"
show 'swim keys trust "$DEMO_DIR/mallory.pub"'
show 'swim verify'
say "A change to trust.yml counts only when signed by a key the pinned set already trusts."
git checkout -q trust.yml; rm -f trust.yml.sig; git checkout -q trust.yml.sig 2>/dev/null
pause

scene "5. Hand the record to an auditor"
say "The chain is plain JSON lines. Signatures are ssh-keygen -Y signatures, the same" \
    "format as git's SSH commit signing, so it can be checked without swim:"
show 'head -1 .swim/chain.jsonl | cut -c1-300; echo ...'
say "Export it somewhere the auditor controls, and verify against that copy:"
show 'swim chain export --to "file:$DEMO_DIR/for-the-auditor" && ls "$DEMO_DIR/for-the-auditor"'
show 'swim verify --against "file:$DEMO_DIR/for-the-auditor" | tail -5'
demo_done
