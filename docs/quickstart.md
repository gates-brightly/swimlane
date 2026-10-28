# swim in an hour

A guided first hour with swim, in a scratch repo, with no cloud
credentials. Each step ends with something to look at, and introduces one
idea when you first need it. The commands are tested: they run, in order,
in swim's own test suite (`internal/e2e/quickstart_test.go`).

You need `swim`, `git` and `bash`, and `ssh-keygen` for step 5:

```
go install github.com/gates-brightly/swimlane/cmd/swim@latest
swim --version
```

`swim version` says if a newer release is out, and the exact line that
installs it.

<!-- quickstart -->

## 1. Install and init

```sh
mkdir swim-hour && cd swim-hour && git init -q
swim init
cat .gitignore
```

`swim init` registers the repo in your config, adds swim's block to
`.gitignore` (lane scripts and swim's state are yours, not the repo's) and
creates `.swim/status.yml`. A **lane** is a numbered slot that holds one
**round**: a script, `lane.N.sh`, that does one piece of work.

## 2. A first round

```sh
swim new 1 "Count the files here"
cat > lane.1.sh <<'EOF'
#!/usr/bin/env bash
# swim: syntax 4
# Round: Count the files here
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage snapshot
snapshot "list the files" ls -la

stage check
run "count them" bash -c 'ls | wc -l'

summary
EOF
swim run 1 --plain
swim log 1
swim status
```

Every `run` is a **step**: its command, output, exit code and time go to
the lane's log (`.swim/logs/agent1.log`). **PASS means exit 0, nothing
more**: read the output to know what happened. The snapshot lists one
file: a round runs in a **staged copy** of what it pins (its script, and
the files its `# Includes:` names), so what runs is what was approved.
The repo itself is `$SWIM_ROOT`. The **stages** (snapshot,
check, change, verify) say where the round is: snapshot and check only
read, change is where guarded work happens.

## 3. A gate fails closed

```sh
cat > lane.1.sh <<'EOF'
#!/usr/bin/env bash
# swim: syntax 4
# Round: Only with a config file
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage check
gate "there is a config file" test -f app.conf

stage change
run "never reached without app.conf" echo "changing things"

summary
EOF
swim run 1 --plain || echo "the round failed, as it should"
swim log 1 | tail -8
```

A failed `run` lets the round go on; a failed **gate** stops it, before
anything changes. Write checks so an empty or unknown answer fails.

## 4. A guard: a dry run until a person says yes

```sh
echo "old data" > scratch.txt
swim new 2 "Remove scratch.txt"
cat > lane.2.sh <<'EOF'
#!/usr/bin/env bash
# swim: syntax 4
# Round: Remove scratch.txt
# Guards: HOUR_ALLOW_RM  remove scratch.txt
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2

stage check
gate "scratch.txt is there" test -f "$SWIM_ROOT/scratch.txt"

stage change
if guard HOUR_ALLOW_RM "remove scratch.txt (quickstart)"; then
  run "remove it" rm "$SWIM_ROOT/scratch.txt"
fi

stage verify
run "what's left" ls "$SWIM_ROOT"

summary
EOF
swim run 2 --plain --no-prompt || echo "exit $?: a dry run"
ls scratch.txt
HOUR_ALLOW_RM=1 swim run 2 --plain
ls scratch.txt 2>/dev/null || echo "scratch.txt is gone"
swim log 2 | grep -E 'SKIP|APPROVED'
```

The **guard** is the destructive step's gate: unapproved, it's a **dry
run** (`SKIP ... dry run`), nothing changes, and the round ends SKIP,
not PASS: it didn't do its work, so `swim run` exits 3 (waiting on an
approval) and a lane after it won't start. On a terminal, `swim run 2`
asks you (`Y/N [N]`); `HOUR_ALLOW_RM=1` approves up front, and the log
records which it was (`APPROVED ... (HOUR_ALLOW_RM=1)`). The header's
`# Guards:` line is the lane's contract: a guard it doesn't declare is
refused. The file it removes is the round's target, so the script names
it in the repo, `$SWIM_ROOT/scratch.txt`.

## 5. Approve ahead, and what voids it

```sh
echo "old data" > scratch.txt
ssh-keygen -q -t ed25519 -N '' -f hour-key
swim keys add me hour-key.pub     # lookup.yml: whose key is whose
swim approve 2 --guards HOUR_ALLOW_RM --key hour-key
swim approve
swim run 2 --plain --no-prompt
ls scratch.txt 2>/dev/null || echo "removed, approved by a signature"
```

`swim approve` signs exactly what will run: the script's bytes, the files
it reads, how it runs and the repo's policy (the **execution manifest**).
`lookup.yml` says whose key is whose. Change the script, and the approval
no longer matches:

```sh
echo "old data" > scratch.txt
echo "# an edit after approval" >> lane.2.sh
swim approve
swim run 2 --plain --no-prompt || echo "exit $?: a dry run"
ls scratch.txt
```

Dry run again: the approval was for the script as it was.

## 6. Two lanes and a dependency

```sh
swim new 3 "Build"
cat > lane.3.sh <<'EOF'
#!/usr/bin/env bash
# swim: syntax 4
# Round: Build
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 3
stage check
run "build" bash -c 'test -f "$SWIM_ROOT/ok.flag" && echo built'
summary
EOF
swim new 4 "Ship the build"
cat > lane.4.sh <<'EOF'
#!/usr/bin/env bash
# swim: syntax 4
# Round: Ship the build
# After: 3
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 4
stage check
run "ship" echo shipped
summary
EOF
swim run 3 4 --plain || true
swim status
touch ok.flag
swim run 3 4 --plain
```

`# After: 3` makes lane 4 wait for lane 3 and need it to pass: with lane 3
failing, lane 4 is **skipped**. Independent lanes run at once.

## 7. A queue and a swimmer

```sh
swim queue add 3 4
swim queue
swim watch --once
swim queue
```

`swim queue add` pins a **batch** to the scripts as they are now, with the
approvals you give it (none here: a queued batch never asks, so its guards
dry-run unless approved). A **swimmer** (`swim watch`) runs batches as their
lanes come free; `--once` runs what's queued and exits. This is how a
session that writes rounds hands them to a machine that runs them.

## 8. Reading the record

```sh
swim report
swim log | tail -5
swim verify --allow-unsigned
```

`swim report` is everything written since you last looked (with
`--earmark`, it remembers where you were). `.swim.log` (`swim log` with no
lane) is the project log, one line per action. `swim verify` checks the **chain of custody**:
every round and approval, hash-linked, so the record can't be quietly
rewritten. This repo signs approvals with `hour-key` but not its record;
with a signing key (`swim config signing_key`), drop `--allow-unsigned`
and an unsigned entry fails it.

<!-- /quickstart -->

## 9. With Claude

swim is built for an AI session to write rounds and a person to run them:

```
swim skill
```

prints how to install the Claude skill that teaches a session swim's loop:
read the record (`swim report --earmark`), write a lane, lint it, check
it with `swim status`, queue it without approving anything, and flag each
risk for you. You
approve and run; the session reads the log and writes the next round.

## Where next

- [docs/README.md](README.md): the rest of the docs, in reading order.
- `swim guide`: the full guide. One section:
  `swim guide | awk '/^WRITING A LANE SCRIPT$/{p=1;print;next} p&&/^[A-Z][A-Z ]+$/{exit} p'`
- [demos/](../demos/README.md): six scripted scenarios, from a guarded change
  to tamper evidence.
