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
