#!/usr/bin/env python3
"""Graph helpers over the lane scripts in the current directory.

  dag.py descendants N [N...]   every lane downstream of the given lanes
  dag.py edges                  number of `# After:` edges
"""
import glob
import re
import sys


def graph():
    after = {}
    for p in glob.glob("lane.*.sh"):
        with open(p) as f:
            src = f.read()
        if re.search(r"^# swim:stub", src, re.M):
            continue
        m = re.search(r"^# After:[ \t]*(.*)$", src, re.M)
        after[int(p.split(".")[1])] = [int(x) for x in (m.group(1).split() if m else [])]
    return after


def descendants(roots):
    kids = {}
    for n, ps in graph().items():
        for p in ps:
            kids.setdefault(p, []).append(n)
    seen, stack = set(), list(roots)
    while stack:
        for c in kids.get(stack.pop(), []):
            if c not in seen:
                seen.add(c)
                stack.append(c)
    return sorted(seen)


if __name__ == "__main__":
    cmd, args = sys.argv[1], [int(x) for x in sys.argv[2:]]
    if cmd == "descendants":
        print(" ".join(map(str, descendants(args))))
    elif cmd == "edges":
        print(sum(len(ps) for ps in graph().values()))
    else:
        sys.exit(f"dag.py: unknown command {cmd}")
