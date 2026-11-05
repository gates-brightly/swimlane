# The tour

An hour with swim in a scratch repo, no credentials. Each step shows one
idea once, and links to its reference page. The commands run, in order, in
swim's test suite. You need `swim`, `git`, `bash` and `ssh-keygen`.

<!-- quickstart -->

## 1. Init

```sh
mkdir swim-hour && cd swim-hour && git init -q
swim init
cat .gitignore
```

`swim init` registers the repo in your config, git-ignores lane scripts and
swim's state, and creates `.swim/status.yml`. ([config](reference/config.md))

## 2. A round

```sh
swim new 1 "Count the files here"
cat > lane.1.sh <<'EOF'
#!/usr/bin/env swim
# swim: syntax 4
# Round: Count the files here

stage snapshot
snapshot "list the files" ls -la

stage check
run "count them" bash -c 'ls | wc -l'
EOF
swim run 1 --plain
swim log 1
swim status
```

Each `run` is a step, logged with its command, output, exit code and time.
PASS means exit 0, nothing more: read the output. The snapshot lists one
file because a round runs in a staged copy of what it pins; the repo itself
is `$SWIM_ROOT`. Stages say where the round is: snapshot and check only
read. ([lanes](reference/lanes.md), [logs](reference/logs.md))

## 3. A gate fails closed

```sh
cat > lane.1.sh <<'EOF'
#!/usr/bin/env swim
# swim: syntax 4
# Round: Only with a config file

stage check
gate "there is a config file" test -f app.conf

stage change
run "never reached without app.conf" echo "changing things"
EOF
swim run 1 --plain || echo "the round failed, as it should"
swim log 1 | tail -8
```

A failed `run` lets the round go on; a failed gate stops it before anything
changes.

## 4. A guard

```sh
echo "old data" > scratch.txt
swim new 2 "Remove scratch.txt"
cat > lane.2.sh <<'EOF'
#!/usr/bin/env swim
# swim: syntax 4
# Round: Remove scratch.txt
# Guards: HOUR_ALLOW_RM  remove scratch.txt

stage check
gate "scratch.txt is there" test -f "$SWIM_ROOT/scratch.txt"

stage change
if guard HOUR_ALLOW_RM "remove scratch.txt (quickstart)"; then
  run "remove it" rm "$SWIM_ROOT/scratch.txt"
fi

stage verify
run "what's left" ls "$SWIM_ROOT"
EOF
swim run 2 --plain --no-prompt || echo "exit $?: a dry run"
ls scratch.txt
HOUR_ALLOW_RM=1 swim run 2 --plain
ls scratch.txt 2>/dev/null || echo "scratch.txt is gone"
swim log 2 | grep -E 'SKIP|APPROVED'
```

Unapproved, the guarded step is a dry run and the round ends SKIP: `swim
run` exits 3, and a lane after it won't start. On a terminal swim asks;
`HOUR_ALLOW_RM=1` approves up front, and the log says which.
([guards](reference/guards.md))

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

`swim approve` signs exactly what will run: the script, the files it pins,
how it runs, and the repo's policy. Change any of it, and the approval no
longer matches ([manifest](reference/manifest.md)):

```sh
echo "old data" > scratch.txt
echo "# an edit after approval" >> lane.2.sh
swim approve
swim run 2 --plain --no-prompt || echo "exit $?: a dry run"
ls scratch.txt
```

## 6. A dependency

```sh
swim new 3 "Build"
cat > lane.3.sh <<'EOF'
#!/usr/bin/env swim
# swim: syntax 4
# Round: Build
stage check
run "build" bash -c 'test -f "$SWIM_ROOT/ok.flag" && echo built'
EOF
swim new 4 "Ship the build"
cat > lane.4.sh <<'EOF'
#!/usr/bin/env swim
# swim: syntax 4
# Round: Ship the build
# After: 3
stage check
run "ship" echo shipped
EOF
swim run 3 4 --plain || true
swim status
touch ok.flag
swim run 3 4 --plain
```

Lane 4 waits for lane 3 to pass; with 3 failing, 4 is skipped. Independent
lanes run at once. ([running](reference/running.md))

## 7. The queue

```sh
swim queue 3 4
swim queue
swim watch --once
swim queue
```

`swim queue` pins a batch to the scripts as they are now, with any
approvals you give it. A swimmer (`swim watch`) runs batches as their lanes
come free: how a session hands work to the machine that runs it.
([queue](reference/queue.md))

## 8. The record

```sh
swim report
swim log | tail -5
swim verify --allow-unsigned
perl -pi -e 's/shipped/SHIPPED/' .swim/logs/agent4.log    # someone edits the record
swim verify --allow-unsigned || echo "the edit was caught"
```

`swim report` is everything since you last looked; `swim log` alone is the
project log. Every round and approval is hash-linked into a chain, so
`swim verify` catches an edit, a removal or a reorder. With a signing key
(`swim config signing_key`), drop `--allow-unsigned`. ([chain](reference/chain.md))

<!-- /quickstart -->

Everything else, one page per topic: [the reference](reference/README.md).
