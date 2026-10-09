gate "audit the DAG" python3 - <<'PY'
import os, re, glob, sys, hashlib
d, me = os.environ["D"], int(os.environ["N"])
run = open(f"{d}/run.id").read().split()[0]
h = lambda s: hashlib.sha256(s.encode()).hexdigest()[:16]
after = {}
for p in glob.glob("lane.*.sh"):
    n = int(p.split(".")[1]); src = open(p).read()
    if re.search(r"^# swim:stub", src, re.M): continue
    m = re.search(r"^# After:[ \t]*(.*)$", src, re.M)
    after[n] = [int(x) for x in (m.group(1).split() if m else [])]
# Lanes in this swim run: the last `run` event in .swim.log that includes this lane.
this_run = set(after)
if os.path.exists(".swim.log"):
    for line in open(".swim.log", encoding="utf-8"):
        f = line.split()
        if len(f) >= 5 and f[2] == "run" and f[3] == "swim":
            lanes = {int(x) for x in f[4].split(",") if x.isdigit()}
            if me in lanes: this_run = lanes
earlier = sorted(set(after) - this_run)
print(f"lanes: {len(after)}  in this swim run: {len(this_run & set(after))}  from an earlier run: {' '.join(map(str, earlier)) or '-'}")
nodes = {}
for n in after:
    if n == me: nodes[n] = (run, float(os.environ["NODE_START"]), None); continue
    f = f"{d}/nodes/{n}.done"
    if os.path.exists(f):
        rid, s, e = open(f).read().split(); nodes[n] = (rid, float(s), float(e))
bad = []

# 1. every node finished, on this run. Lanes descending from swim 1 carry
#    run.id; lanes whose ancestry never reaches swim 1 carry their own indep: id.
main = {1}
for n in sorted(after):                      # parents are always lower-numbered
    if any(p in main for p in after[n]): main.add(n)
print(f"lineage: {len(main)} lanes descend from swim 1, {len(set(after) - main)} only from independent roots")
for n in sorted(after):
    if n not in nodes: bad.append(f"swim {n}: no done marker"); continue
    rid = nodes[n][0]
    want = run if n in main else "indep:*"
    if not (rid == run if n in main else rid.startswith("indep:")):
        bad.append(f"swim {n}: run {rid}, expected {want}")

# 2. every edge: child started after parent finished
edges = ok = cross = 0
for n in sorted(after):
    for p in after[n]:
        if n not in nodes or p not in nodes: continue
        edges += 1
        gap = nodes[n][1] - nodes[p][2]
        if gap < 0: bad.append(f"edge {p}->{n} violated: child started {-gap:.2f}s before parent finished")
        else:
            ok += 1; cross += p not in this_run and n in this_run
print(f"edges: {edges} checked, {ok} ok ({cross} with the parent from an earlier run), {edges - ok} violated")

# 3. data flow: recompute every synthetic node's value from the graph
memo = {}
def expect(n):
    if n not in memo:
        if n < 10:                                    # real-work lanes carry no value; hash their run id
            memo[n] = h(f"{n}:" + nodes[n][0])
        else:
            ps = sorted(after[n]); memo[n] = h(f"{n}|" + "|".join(expect(p) for p in ps)) if ps else h(f"{n}:root")
    return memo[n]
vals = vbad = 0
for n in sorted(after):
    vf = f"{d}/nodes/{n}.val"
    if 10 <= n < me and n in nodes and os.path.exists(vf):
        vals += 1
        try: want = expect(n)
        except KeyError as k: bad.append(f"swim {n}: can't recompute (missing {k})"); continue
        got = open(vf).read().strip()
        if got != want: vbad += 1; bad.append(f"swim {n}: value {got} != expected {want} (read a parent early?)")
print(f"values: {vals} recomputed, {vals - vbad} match")

# 4. independent roots in this run start together
roots = [n for n in sorted(after) if not after[n] and n in this_run and n in nodes]
if len(roots) > 1:
    spread = max(nodes[r][1] for r in roots) - min(nodes[r][1] for r in roots)
    print(f"roots in this run {roots}: start spread {spread:.2f}s  {'ok' if spread < 2 else 'SERIALISED?'}")
    if spread >= 2: bad.append(f"independent roots started {spread:.2f}s apart in one run")
else:
    print(f"roots in this run: {roots or '-'} (need 2+ to test parallel roots)")

# 5. scheduler latency: start - last parent finish, for nodes whose parents all ran in this run
lat = sorted((nodes[n][1] - max(nodes[p][2] for p in after[n]), n) for n in after
             if n in this_run and n in nodes and after[n] and all(p in this_run and p in nodes for p in after[n]))
if lat:
    xs = [x for x, _ in lat]
    print(f"scheduler latency (start - last parent done), {len(xs)} nodes: "
          f"median {xs[len(xs)//2]:.2f}s  p95 {xs[int(len(xs)*.95)]:.2f}s  max {xs[-1]:.2f}s (swim {lat[-1][1]})")
cur = {n: v for n, v in nodes.items() if n in this_run}
t0 = min(s for _, s, _ in cur.values()); end = max(e or s for _, s, e in cur.values())
# critical path by recorded durations, vs actual makespan
cp = {}
def path(n):
    if n not in cp:
        s, e = nodes[n][1], nodes[n][2] or nodes[n][1]
        cp[n] = (e - s) + max([path(p) for p in after[n] if p in cur] or [0])
    return cp[n]
crit = max(path(n) for n in cur)
print(f"makespan {end - t0:.2f}s vs critical-path work {crit:.2f}s -> scheduling overhead {end - t0 - crit:.2f}s")

width = int((end - t0) / .25) + 1
print("\ntimeline of this run (one char = 0.25s)")
for n in sorted(nodes):
    _, s, e = nodes[n]
    if n not in this_run: print(f"swim {n:>2}   (earlier run)"); continue
    e = e or end
    a = int((s - t0) / .25); b = max(int((e - t0) / .25), a + 1)
    ps = ' '.join(map(str, after[n])) or '-'
    print(f"swim {n:>2} |{' ' * a}{'#' * (b - a)}{' ' * max(width - b, 0)}| {s - t0:5.2f}s -> {e - t0:5.2f}s  after: {ps if len(ps) < 40 else ps[:37] + '...'}")
print("\n" + ("PASS: every node done, every edge honoured, every value matches" if not bad else f"FAIL ({len(bad)}):\n  " + "\n  ".join(bad[:40])))
sys.exit(1 if bad else 0)
PY
