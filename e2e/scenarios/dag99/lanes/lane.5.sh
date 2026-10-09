#!/usr/bin/env bash
# Round: DAG child C (of 1): extract every link
# Job:   e07e8da0-451c-44d1-ba98-df2151d7ba72
# After: 1
# Lane:  swim 5    Written: 2026-10-09
#
# Goal:
#   Every <a href> in combined.html, per source page, into links.tsv. ~0.1s: the
#   fast branch, so swim 7 waits on swim 3 long after this is done.
#
# Steps:
#   0. Clear this node's done marker; gate: every parent (swim 1) finished, for this run, before this node started
#   1. Simulated work (0.1s)
#   2. Gate: extract links.tsv (source, kind, href)
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/5.done
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
lane_init 5

D=.scenario/dag
N=5
PARENTS="1"
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

run "simulated work (0.1s)" sleep 0.1
gate "extract links.tsv" python3 - <<'PY'
import os, re
from html.parser import HTMLParser
class Links(HTMLParser):
    def __init__(self):
        super().__init__(); self.src, self.rows = None, []
    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag == "section" and "data-source" in a: self.src = a["data-source"]
        elif tag == "a" and a.get("href") and self.src:
            self.rows.append((self.src, "external" if re.match(r"https?://", a["href"]) else "internal", a["href"]))
d = os.environ["D"]
p = Links(); p.feed(open(f"{d}/combined.html", encoding="utf-8").read())
with open(f"{d}/.links.tmp", "w") as f:
    f.write("source\tkind\thref\n" + "".join("\t".join(r) + "\n" for r in p.rows))
os.replace(f"{d}/.links.tmp", f"{d}/links.tsv")
print(f"{len(p.rows)} links: {sum(r[1]=='external' for r in p.rows)} external")
assert p.rows, "no links found"
PY

gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(python3 -c "import time; print(f\"{time.time():.3f}\")")" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
