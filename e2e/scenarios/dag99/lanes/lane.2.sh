#!/usr/bin/env bash
# Round: DAG child A (of 1): combined.html to Markdown
# Job:   7353ee29-f742-41b0-bcd1-ebab167c75a2
# After: 1
# Lane:  swim 2    Written: 2026-10-09
#
# Goal:
#   Convert swim 1's combined.html to combined.md (stdlib-only converter). ~0.3s.
#
# Steps:
#   0. Clear this node's done marker; gate: every parent (swim 1) finished, for this run, before this node started
#   1. Private copy of the input; simulated work (0.3s); convert
#   2. Gate: one H1 per page, no leftover tags
#   z. E2E_FAIL toggle; on success write .scenario/dag/nodes/2.done
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
lane_init 2

D=.scenario/dag
N=2
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

IN=$D/input-2.$SWIM_JOB.html
export IN
gate "copy input to $IN" cp "$D/combined.html" "$IN"
run "simulated work (0.3s)" sleep 0.3
gate "convert html -> markdown" env OUT="$D" python3 - <<'PY'
import os, re, html
from html.parser import HTMLParser

class MD(HTMLParser):
    SKIP = {"script", "style", "head", "noscript", "svg"}
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.out, self.skip, self.lists, self.href, self.pre = [], 0, [], None, 0
    def w(self, s): self.out.append(s)
    def block(self): self.w("\n\n")
    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if tag in self.SKIP: self.skip += 1; return
        if self.skip: return
        if re.fullmatch(r"h[1-6]", tag): self.block(); self.w("#" * int(tag[1]) + " ")
        elif tag in ("p", "div", "section", "dl", "table"): self.block()
        elif tag in ("dt", "tr"): self.w("\n")
        elif tag == "dd": self.w("\n: ")
        elif tag in ("td", "th"): self.w(" | ")
        elif tag == "br": self.w("  \n")
        elif tag == "hr": self.block(); self.w("---"); self.block()
        elif tag in ("strong", "b"): self.w("**")
        elif tag in ("em", "i"): self.w("*")
        elif tag == "code" and not self.pre: self.w("`")
        elif tag == "pre": self.pre += 1; self.block(); self.w("```\n")
        elif tag in ("ul", "ol"): self.lists.append([tag, 0]); self.w("\n")
        elif tag == "li":
            depth = max(len(self.lists) - 1, 0)
            kind = self.lists[-1] if self.lists else ["ul", 0]
            kind[1] += 1
            self.w("\n" + "  " * depth + (f"{kind[1]}. " if kind[0] == "ol" else "- "))
        elif tag == "a": self.href = a.get("href"); self.w("[")
        elif tag == "img": self.w(f"![{a.get('alt', '')}]({a.get('src', '')})")
    def handle_endtag(self, tag):
        if tag in self.SKIP: self.skip = max(self.skip - 1, 0); return
        if self.skip: return
        if re.fullmatch(r"h[1-6]", tag) or tag in ("p", "div", "section", "table"): self.block()
        elif tag in ("strong", "b"): self.w("**")
        elif tag in ("em", "i"): self.w("*")
        elif tag == "code" and not self.pre: self.w("`")
        elif tag == "pre": self.pre -= 1; self.w("\n```"); self.block()
        elif tag in ("ul", "ol"):
            if self.lists: self.lists.pop()
            self.block()
        elif tag == "a":
            self.w(f"]({self.href})" if self.href else "]"); self.href = None
    def handle_data(self, data):
        if self.skip: return
        self.w(data if self.pre else re.sub(r"\s+", " ", data))

src = open(os.environ["IN"], encoding="utf-8").read()
p = MD(); p.feed(src); p.close()
md = "".join(p.out)
md = re.sub(r"[ \t]+\n", "\n", md)          # trailing spaces (keeps none; <br> already newline)
md = re.sub(r"\n[ \t]+(?=[^-\d\s])", "\n", md)  # stray leading spaces outside lists
md = re.sub(r"\n{3,}", "\n\n", md).strip() + "\n"
dst = os.path.join(os.environ["OUT"], "combined.md")
open(dst, "w", encoding="utf-8").write(md)
print(f"wrote {dst}: {len(md.splitlines())} lines, {len(md)} chars")
PY
gate "one H1 per source page" bash -c 'h=$(grep -c "^# " "$D/combined.md"); s=$(grep -c "<section data-source" "$IN"); echo "h1=$h sections=$s"; test "$h" -ge "$s" -a "$h" -gt 0'
gate "no leftover HTML tags" bash -c '! grep -nE "</?(div|p|span|section|body|html)[ >]" "$D/combined.md"'

gate "simulated failure off (E2E_FAIL='${E2E_FAIL:-}')" bash -c 'case " ${E2E_FAIL:-} " in *" $N "*) echo "E2E_FAIL includes $N"; exit 1;; esac'
if ! any_failed; then
  gate "mark node done" bash -c 'printf "%s %s %s\n" "$RUN_ID" "$NODE_START" "$(python3 -c "import time; print(f\"{time.time():.3f}\")")" > "$D/nodes/.$N.tmp" && mv "$D/nodes/.$N.tmp" "$D/nodes/$N.done" && cat "$D/nodes/$N.done"'
fi

summary
