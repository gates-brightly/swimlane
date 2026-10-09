#!/usr/bin/env bash
# Round: DAG wide join (of 4,6,7,8): bundle.md
# Job:   4b01289e-7ad9-4754-b975-886bf0da5c08
# After: 4 6 7 8
# Lane:  swim 9    Written: 2026-10-09
#
# Goal:
#   Four parents at three different depths plus the independent root.
#
# Steps:
#   0. Clear this node's done marker; gate: every parent (swim 4 6 7 8) finished, for this run, before this node started
#   1. Gate: all four parent outputs present; build bundle.md
#   2. Verify: every section present
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/9.done
#
# Guard flags this round honours (flag: action, date, reason):
#   (none)
# Test toggle (not a guard; nothing destructive):
#   E2E_FAIL="3 5"   space-separated lanes that fail on purpose (to watch skips propagate)
#
# DAG (each lane's `# After:` line), lanes 1-9:
#   1 -> 2, 3, 5    2 -> 4, 6    3 -> 4, 7    5 -> 7
#   4, 6, 7, 8 -> 9    9 -> 10..19 (generated)    8 has no parents (independent root)
# Shared dir .scenario/dag/: run.id (swim 1), nodes/N.done ("<run> <start> <end>").
#
# Part of the e2e scenario e2e/scenarios/dag99 (run: e2e/run.sh dag99).
# Never edit this file while it may be running: `swim status` first.
# Do not use `set -e`: failed checks keep going; only gates stop the round.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init 9

D=.scenario/dag
N=9
PARENTS="4 6 7 8"
NODE_START=$(python3 -c 'import time; print(f"{time.time():.3f}")')
export D N PARENTS NODE_START
mkdir -p "$D/nodes"
run "clear own done marker" rm -f "$D/nodes/$N.done"
gate "parents finished before this node started (swim $PARENTS)" python3 - $PARENTS <<'PY'
import os, sys
d, start = os.environ["D"], float(os.environ["NODE_START"])
run = open(f"{d}/run.id").read().split()[0] if os.path.exists(f"{d}/run.id") else None
ok = True
for p in sys.argv[1:]:
    path = f"{d}/nodes/{p}.done"
    if not os.path.exists(path):
        print(f"swim {p}: no done marker  BAD"); ok = False; continue
    rid, s, e = open(path).read().split()
    gap = start - float(e)
    fresh = (rid.startswith("indep:") and gap < 600) or rid == run
    good = fresh and gap >= 0
    print(f"swim {p}: run={rid}  finished {gap:6.2f}s before this node started  {'ok' if good else 'BAD (stale run)' if not fresh else 'BAD (overlap)'}")
    ok &= good
sys.exit(0 if ok else 1)
PY
RUN_ID=$(cut -d' ' -f1 "$D/run.id")
export RUN_ID

gate "parent outputs present" bash -c 'for f in report.md wordfreq.md domains.md headers.md; do test -s "$D/$f" || { echo "missing $f"; exit 1; }; wc -l "$D/$f"; done'
gate "build bundle.md" bash -c '{ echo "# Swim DAG demo bundle"; echo; echo "Run: \`$RUN_ID\`"; echo; for f in headers.md wordfreq.md domains.md; do cat "$D/$f"; echo; done; sed "1s/^# /## /" "$D/report.md"; } > "$D/.bundle.tmp" && mv "$D/.bundle.tmp" "$D/bundle.md" && wc -l "$D/bundle.md"'
run "bundle has every section" bash -c 'for h in "Response headers" "Word frequency" "External link domains" "Stats (swim 3)" "Content (swim 2)"; do grep -q "^## $h" "$D/bundle.md" && echo "ok  $h" || { echo "MISSING  $h"; exit 1; }; done'

gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(python3 -c "import time; print(f\"{time.time():.3f}\")")" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
