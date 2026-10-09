#!/usr/bin/env bash
# Round: DAG child B (of 1): per-page link and word stats
# Job:   127c5d37-ca9e-441f-9256-1a97d5186feb
# After: 1
# Lane:  swim 3    Written: 2026-10-09
#
# Goal:
#   Per-page title, words, internal/external link counts into stats.json. ~0.5s:
#   the slow branch, so 4 and 7 wait on it after their other parents finish.
#
# Steps:
#   0. Clear this node's done marker; gate: every parent (swim 1) finished, for this run, before this node started
#   1. Private copy of the input; simulated work (0.5s)
#   2. Gate: compute stats.json
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/3.done
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
lane_init 3

D=.scenario/dag
N=3
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

IN=$D/input-3.$SWIM_JOB.html
export IN
gate "copy input to $IN" cp "$D/combined.html" "$IN"
run "simulated work (0.5s)" sleep 0.5
gate "compute stats.json" python3 - <<'PY'
import os, re, json
from html.parser import HTMLParser

class Stats(HTMLParser):
    SKIP = {"script", "style", "head", "noscript", "svg"}
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.pages, self.cur, self.skip, self.in_h1 = [], None, 0, False
    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag in self.SKIP: self.skip += 1
        if tag == "section" and "data-source" in a:
            self.cur = {"source": a["data-source"], "title": "", "words": 0, "links_internal": 0, "links_external": 0}
            self.pages.append(self.cur)
        elif self.cur is not None and tag == "h1" and not self.cur["title"]:
            self.in_h1 = True
        elif self.cur is not None and tag == "a" and a.get("href"):
            self.cur["links_external" if re.match(r"https?://", a["href"]) else "links_internal"] += 1
    def handle_endtag(self, tag):
        if tag in self.SKIP: self.skip = max(self.skip - 1, 0)
        if tag == "h1": self.in_h1 = False
    def handle_data(self, data):
        if self.skip or self.cur is None: return
        if self.in_h1: self.cur["title"] += data.strip()
        self.cur["words"] += len(data.split())

d = os.environ["D"]
p = Stats(); p.feed(open(os.environ["IN"], encoding="utf-8").read()); p.close()
out = {"run_id": os.environ["RUN_ID"], "job": os.environ["SWIM_JOB"], "pages": p.pages}
tmp = os.path.join(d, ".stats.tmp")
json.dump(out, open(tmp, "w"), indent=2)
os.replace(tmp, os.path.join(d, "stats.json"))
for pg in p.pages:
    print(f"{pg['source']:8} words={pg['words']:5} int={pg['links_internal']:3} ext={pg['links_external']:3}  {pg['title']}")
assert p.pages, "no pages found"
PY

gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(python3 -c "import time; print(f\"{time.time():.3f}\")")" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
