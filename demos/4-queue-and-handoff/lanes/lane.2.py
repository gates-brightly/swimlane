#!/usr/bin/env python3
# swim: syntax 3
# Round:  Clear the sessions the leak left behind
# After:  1
# Owner:  planner (AI session)
# Guards: SESSIONS_ALLOW_PURGE  delete sessions older than the 2.4.1 deploy
#
# A lane in Python, with swim's SDK (sdk/python): the same stages, steps,
# guards and log as a bash lane.
import subprocess
import swim

def stale():
    out = subprocess.run(["fakecloud", "select", "sessions", "stale"], capture_output=True, text=True).stdout
    return [l for l in out.splitlines() if l]

with swim.round() as r:
    r.stage("snapshot")
    r.snapshot("sessions before", ["fakecloud", "select", "sessions"])

    r.stage("check")
    with r.step("count stale sessions") as s:   # runs here, in Python
        n = len(stale())
        s.log(f"{n} stale sessions")

    r.stage("change")
    if r.guard("SESSIONS_ALLOW_PURGE", "delete sessions older than the 2.4.1 deploy"):
        r.run("delete stale sessions", ["fakecloud", "delete", "sessions", "stale"])

    r.stage("verify")
    with r.step("no stale sessions left") as s:
        s.require(not stale(), f"{len(stale())} stale sessions remain")
