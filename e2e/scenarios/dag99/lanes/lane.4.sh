#!/usr/bin/env bash
# Round: DAG join A+B (of 2,3): report.md
# Job:   7a2fd47d-e445-4c07-944a-b7519b8ef4ff
# After: 2 3
# Lane:  swim 4    Written: 2026-10-09
#
# Goal:
#   Fan-in of two siblings: stats table (swim 3) + converted content (swim 2).
#
# Steps:
#   0. Clear this node's done marker; gate: every parent (swim 2 3) finished, for this run, before this node started
#   1. Gate: build report.md
#   2. Verify: stats table and a section per page
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/4.done
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
lane_init 4

D=.scenario/dag
N=4
PARENTS="2 3"
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

gate "build report.md" python3 - <<'PY'
import os, json
d = os.environ["D"]
stats = json.load(open(os.path.join(d, "stats.json")))
md = open(os.path.join(d, "combined.md"), encoding="utf-8").read()
rows = ["| source | title | words | internal links | external links |", "|---|---|---:|---:|---:|"]
for p in stats["pages"]:
    rows.append(f"| {p['source']} | {p['title']} | {p['words']} | {p['links_internal']} | {p['links_external']} |")
tot = lambda k: sum(p[k] for p in stats["pages"])
rows.append(f"| **total** | {len(stats['pages'])} pages | {tot('words')} | {tot('links_internal')} | {tot('links_external')} |")
body = md.replace("\n# ", "\n## ").removeprefix("# ")
report = (f"# Swim diamond demo report\n\nRoot run: `{stats['run_id']}`  \nJoin job: `{os.environ['SWIM_JOB']}`\n\n"
          "## Stats (swim 3)\n\n" + "\n".join(rows) + "\n\n## Content (swim 2)\n\n## " + body)
open(os.path.join(d, "report.md"), "w", encoding="utf-8").write(report)
print(f"wrote {d}/report.md: {len(report.splitlines())} lines")
PY

run "report has stats table and a section per page" bash -c '
  n=$(python3 -c "import json;print(len(json.load(open(\"$D/stats.json\"))[\"pages\"]))")
  grep -q "^| \*\*total\*\*" "$D/report.md" && h=$(grep -c "^## " "$D/report.md") && echo "pages=$n h2=$h" && test "$h" -ge $((n + 2))'

gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(python3 -c "import time; print(f\"{time.time():.3f}\")")" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
