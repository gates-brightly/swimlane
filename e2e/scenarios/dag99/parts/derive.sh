# Run id comes from the parents, not run.id: a node whose ancestry never reaches
# swim 1 can start before swim 1 writes this run's run.id.
RUN_ID=$(python3 - $PARENTS <<'PY'
import os, sys
d = os.environ["D"]
rids = {open(f"{d}/nodes/{p}.done").read().split()[0] for p in sys.argv[1:]}
main = sorted(r for r in rids if not r.startswith("indep:"))
print(main[0] if main else "indep:" + os.environ["SWIM_JOB"][:8])
PY
)
