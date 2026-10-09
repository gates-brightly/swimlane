#!/usr/bin/env bash
# Round: DAG cross-branch join (of 3,5): domains, reconciled with stats
# Job:   1761bace-f44d-401e-8175-c92907acf66a
# After: 3 5
# Lane:  swim 7    Written: 2026-10-09
#
# Goal:
#   Joins a slow branch (3) and a fast one (5): external-link counts from
#   links.tsv must equal stats.json per page; then domains.md.
#
# Steps:
#   0. Clear this node's done marker; gate: every parent (swim 3 5) finished, for this run, before this node started
#   1. Gate: links.tsv and stats.json agree on external links per page
#   2. Gate: domains.md
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/7.done
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
lane_init 7

D=.scenario/dag
N=7
PARENTS="3 5"
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

gate "links.tsv (swim 5) agrees with stats.json (swim 3)" python3 - <<'PY'
import os, json, csv, collections, sys
d = os.environ["D"]
stats = {p["source"]: p["links_external"] for p in json.load(open(f"{d}/stats.json"))["pages"]}
rows = list(csv.DictReader(open(f"{d}/links.tsv"), delimiter="\t"))
ext = collections.Counter(r["source"] for r in rows if r["kind"] == "external")
bad = 0
for src in sorted(set(stats) | set(ext)):
    m = "ok" if stats.get(src, 0) == ext.get(src, 0) else "MISMATCH"; bad += m != "ok"
    print(f"{src:8} stats={stats.get(src, 0):3}  links.tsv={ext.get(src, 0):3}  {m}")
sys.exit(1 if bad else 0)
PY
gate "domains.md" python3 - <<'PY'
import os, csv, collections
from urllib.parse import urlparse
d = os.environ["D"]
rows = [r for r in csv.DictReader(open(f"{d}/links.tsv"), delimiter="\t") if r["kind"] == "external"]
c = collections.Counter(urlparse(r["href"]).netloc for r in rows)
out = "## External link domains (swim 7)\n\n| domain | links |\n|---|---:|\n" + "".join(f"| {k} | {v} |\n" for k, v in c.most_common())
open(f"{d}/domains.md", "w").write(out)
print(out)
PY

gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(python3 -c "import time; print(f\"{time.time():.3f}\")")" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
