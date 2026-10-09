#!/usr/bin/env bash
# Round: DAG grandchild (of 2): word frequency from Markdown
# Job:   5f181388-34b3-4020-8a2f-26e5ab37d638
# After: 2
# Lane:  swim 6    Written: 2026-10-09
#
# Goal:
#   Grandchild via child A: top words in combined.md into wordfreq.md. ~0.2s.
#
# Steps:
#   0. Clear this node's done marker; gate: every parent (swim 2) finished, for this run, before this node started
#   1. Simulated work (0.2s)
#   2. Gate: wordfreq.md (top 15, links and stopwords removed)
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/6.done
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
lane_init 6

D=.scenario/dag
N=6
PARENTS="2"
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

run "simulated work (0.2s)" sleep 0.2
gate "word frequency -> wordfreq.md" python3 - <<'PY'
import os, re, collections
d = os.environ["D"]
md = open(f"{d}/combined.md", encoding="utf-8").read()
md = re.sub(r"\]\([^)]*\)", "]", md)                       # drop link targets
stop = set("the and that this with from have been were they their them there which what when your about into will would could should these those other than then also such only some more most very upon over under after before while being does done".split())
words = [w for w in re.findall(r"[a-z][a-z']{3,}", md.lower()) if w not in stop]
top = collections.Counter(words).most_common(15)
out = "## Word frequency (swim 6)\n\n| word | count |\n|---|---:|\n" + "".join(f"| {w} | {c} |\n" for w, c in top)
open(f"{d}/wordfreq.md", "w").write(out)
print(out)
assert top, "no words"
PY

gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(python3 -c "import time; print(f\"{time.time():.3f}\")")" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
