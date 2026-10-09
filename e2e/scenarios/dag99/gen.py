#!/usr/bin/env python3
"""Write lanes 10..99 of the dag99 scenario into the current directory.

Lanes 1..9 (lanes/lane.N.sh) do real work and must already be in place:
their `# After:` lines are read to find the sinks lane 99 waits on.

  10..19  fan-out: ten children of lane 9
  20..22  independent roots (no parents)
  23..37  deep chain, 15 lanes
  38      wide fan-in: 13 parents (10..22)
  39..98  random: 1-4 lower-numbered parents (fixed seed, so the graph is stable)
  99      audit: waits on every sink, checks every edge and value

Each generated lane gets a fresh job id, so every run is a new set of rounds.
"""
import os
import random
import re
import uuid

HERE = os.path.dirname(os.path.abspath(__file__))
SEED = 20261009


def topology():
    rnd = random.Random(SEED)
    after, dur, kind = {}, {}, {}
    for n in range(10, 20):
        after[n] = [9]; kind[n] = "fan-out of 9"
    for n in (20, 21, 22):
        after[n] = []; kind[n] = "independent root"
    after[23] = [19]; kind[23] = "chain head"
    for n in range(24, 38):
        after[n] = [n - 1]; kind[n] = "chain"
    after[38] = list(range(10, 23)); kind[38] = "wide fan-in (13 parents)"
    for n in range(39, 99):
        pool = list(range(10, n)) + [2, 3, 5, 6, 7, 8]
        k = rnd.choice([1, 1, 2, 2, 2, 3, 3, 4])
        recent = [p for p in pool if p >= n - 12]
        ps = set(rnd.sample(recent, min(k - 1, len(recent)))) | {rnd.choice(pool)}
        after[n] = sorted(ps); kind[n] = "random"
    for n in after:
        dur[n] = 0.1 if kind[n] == "chain" else round(rnd.uniform(0.1, 0.5), 1)
    return after, dur, kind


def part(name):
    with open(os.path.join(HERE, "parts", name)) as f:
        return f.read()


def main():
    after, dur, kind = topology()
    tmpl, pcheck, derive, audit = part("node.tmpl"), part("parent_check.sh"), part("derive.sh"), part("audit.sh")

    for n in range(10, 99):
        ps = " ".join(map(str, after[n]))
        s = tmpl
        for k, v in {
            "@ROUND@": f"DAG99 node {n} ({kind[n]}): after {ps or 'nothing'}",
            "@JOB@": str(uuid.uuid4()), "@AFTER@": ps, "@N@": str(n), "@KIND@": kind[n],
            "@DUR@": str(dur[n]),
            "@PARENTS_TXT@": f"swim {ps}" if ps else "none",
            "@PARENT_CHECK@": pcheck if ps else "",
            "@RUN_ID@": derive if ps else 'RUN_ID="indep:${SWIM_JOB:0:8}"\n',
        }.items():
            s = s.replace(k, v)
        with open(f"lane.{n}.sh", "w") as f:
            f.write(s)

    lane_after = dict(after)
    for n in range(1, 10):
        with open(f"lane.{n}.sh") as f:
            m = re.search(r"^# After:[ \t]*(.*)$", f.read(), re.M)
        lane_after[n] = [int(x) for x in m.group(1).split()]
    has_child = {p for ps in lane_after.values() for p in ps}
    sinks = " ".join(str(n) for n in range(1, 99) if n not in has_child)
    job = uuid.uuid4()
    with open("lane.99.sh", "w") as f:
        f.write(f'''#!/usr/bin/env bash
# Round: DAG99 audit: every node done, every edge honoured, every value matches
# Job:   {job}
# After: {sinks}
# Lane:  swim 99
#
# Goal:
#   Audit the 99-lane DAG. Waits on every sink, so it runs last.
#   Reads every lane's `# After:` line, nodes/N.done and N.val, and .swim.log
#   (which lanes were in this swim run). Checks: all nodes done on the right
#   run id; every edge finished-before-started; every synthetic value
#   recomputes from the graph (data flow, not just timing); independent roots
#   started together. Reports scheduler latency and makespan vs critical path.
#
# Steps:
#   0. Clear own marker; gate: every sink finished, for this run, before this started
#   1. Gate: audit the DAG (prints a timeline of this run)
#
# Guard flags this round honours (flag: action, date, reason):
#   (none)
#
# Part of the e2e scenario e2e/scenarios/dag99 (run: e2e/run.sh dag99).
# Do not use `set -e`: failed checks keep going; only gates stop the round.

_swim_lib=$("${{SWIM_BIN:-swim}}" lib) || {{ echo "swim not found on PATH" >&2; exit 1; }}
eval "$_swim_lib"
lane_init 99

D=.scenario/dag
N=99
PARENTS="{sinks}"
NODE_START=$(python3 -c 'import time; print(f"{{time.time():.3f}}")')
export D N PARENTS NODE_START
mkdir -p "$D/nodes"
run "clear own marker" rm -f "$D/nodes/$N.done"
{pcheck}{derive}export RUN_ID

{audit}
summary
''')


if __name__ == "__main__":
    main()
