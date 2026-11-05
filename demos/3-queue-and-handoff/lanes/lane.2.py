#!/usr/bin/env python3
# swim: syntax 4
# Round:  Clear the sessions the leak left behind
# After:  1
# Owner:  planner (AI session)
# Guards: SESSIONS_ALLOW_PURGE  delete sessions older than the 2.4.1 deploy
#
# A lane in Python. There's no SDK: steps are swim commands, so the stages,
# guards and log are the same as a bash lane's.
import os
import subprocess

def swim(*a):
    return subprocess.run([os.environ.get("SWIM_BIN", "swim"), *a]).returncode

def stale():
    out = subprocess.run(["fakecloud", "select", "sessions", "stale"], capture_output=True, text=True).stdout
    return [l for l in out.splitlines() if l]

swim("stage", "snapshot")
swim("snapshot", "sessions before", "--", "fakecloud", "select", "sessions")

swim("stage", "check")
swim("gate", "sessions table exists", "--", "fakecloud", "rows", "sessions")
print(f"{len(stale())} stale sessions", flush=True)   # plain Python, logged with the round

swim("stage", "change")
if swim("guard", "SESSIONS_ALLOW_PURGE", "delete sessions older than the 2.4.1 deploy") == 0:
    swim("run", "delete stale sessions", "--", "fakecloud", "delete", "sessions", "stale")

swim("stage", "verify")
swim("run", "no stale sessions left", "--", "bash", "-c", '[ -z "$(fakecloud select sessions stale)" ]')
