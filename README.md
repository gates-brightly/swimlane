# swim

> **A feature must save time, reduce risk or reduce cognitive load in your actual work.**
>
> That's the bar every command in swim has to clear. What doesn't clear it
> doesn't get built, and what stops clearing it gets cut.

[![test](https://github.com/gates-brightly/swimlane/actions/workflows/test.yml/badge.svg)](https://github.com/gates-brightly/swimlane/actions/workflows/test.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**An operator-in-the-loop runner for ops work. Your AI writes the scripts.
You run them, with your access. The logs carry the results back.**

swim is a single binary for one engineer doing infrastructure work with an
AI session: migrations, cutovers, prod diagnostics, anything that has to
run where the AI can't reach. The AI writes each piece of work as a small
bash script (a *lane*). You run it from your own shell, with your SSO
session, your VPN and your keys. swim keeps a log of every step that the
AI reads back when it needs to, and nothing more.

It's a tool, not a platform. No server, no accounts, nothing your team has
to adopt. Like k9s, it pays off the day you install it, whether anyone
else uses it or not.

## Four ideas

That's all you need to know to run swim:

| | |
|---|---|
| **Lane** | A piece of work: a numbered script, `lane.N.sh`, that your AI writes. |
| **Round** | One go at that work, run by you, from your shell, with your access. |
| **Guard** | A step that needs your approval. Until you give it, the step is a dry run. |
| **Log** | What happened, step by step, written for the AI to read back. |

Everything else (gates, dependencies, the queue, signed approvals, the
chain of custody) is built from these four. It's in `swim guide` when you
need it, and you don't need it on day one.

## How it speeds you up

- **Queue the next job while the long one runs.** A migration ties up lane
  1 for an hour. Lanes 2 and 3 are ready, and lane 3 depends on 2: queue
  them, and swim runs them alongside, each once what it waits on has
  passed. The dependency graph lives in the scripts (`# After: 2`), not in
  your head, and `# Locks:` keeps lanes that touch the same thing apart.
- **The AI's context stays on the task.** The AI doesn't sit watching a
  job's output. It writes the round, you run it, and it comes back only
  when something fails, reading a compact, step-by-step log
  (`swim log 2`) instead of a terminal dump pasted into chat.
- **Catch up cold.** Lane scripts, status and logs live in the repo, not
  in a chat. A fresh session (or you, tomorrow) starts from `swim report`
  and picks up where the work stands.
- **Your tooling, your way.** Lanes are plain bash run from your shell, so
  nothing has to be rebuilt for an agent: your aliases, your profiles,
  your break-glass account.
- **Destructive steps wait for you.** A guarded step is a dry run until
  you say yes, at the prompt, with a flag, or ahead of time. Approve a
  batch once and walk away.

## A working session

```sh
swim run 1                       # the long one: a 40-minute data migration
```

Meanwhile, in the AI session, the next two rounds are written for another
service: lane 2 deploys it, lane 3 (`# After: 2`) flips its traffic,
behind a guard.

```sh
swim status                      # what each lane waits on, and its guards
swim queue 2 3 --note "orders cutover"
swim watch                       # the swimmer: runs the batch next to lane 1, 3 after 2
```

Lane 3's guard waits on the swimmer's screen for your `y`. Then lane 2
fails. Only now does the AI need to look:

```sh
swim                             # what needs you: lane 2's failure, and the command for it
swim report --earmark            # everything since you last looked, then marks your place
swim log 2                       # the failed round, step by step
```

The AI fixes lane 2, you queue 2 and 3 again, and lane 3 follows.

## Install

swim is one static binary, for Linux and macOS (amd64, arm64). Each release
publishes them; the installer picks this machine's, checks its checksum and
puts it in /usr/local/bin (if writable) or ~/.local/bin:

```sh
curl -fsSL https://github.com/gates-brightly/swimlane/releases/latest/download/install.sh | sh
swim --version                                # e.g. swim 0.20261102.1
```

`SWIM_VERSION=v0.20261102.1` pins a release. No Go toolchain needed, so it's
the quick way into a container. To build from source instead (Go 1.27.1 or
newer): `go install github.com/gates-brightly/swimlane/cmd/swim@latest`.

Pinning a version, building from a checkout, and what each release
promises: [install and versions](docs/usage.md#install-and-versions).

To teach your AI session the loop, run `swim skill` (the Claude skill) or
`swim guide` (the full guide for any agent).

## Your first five minutes

No cloud credentials, nothing to configure: a scratch repo, your AI and
swim. By the end you'll have seen what swim is for:

> **Your AI prepares the operational work and makes sense of what
> happened, without ever being given the authority to run it.**

**1. Install** swim ([above](#install)). **2. Make a scratch repo:**

<!-- quickstart -->
```sh
mkdir swim-try && cd swim-try && git init -q
swim init
touch build.tmp cache.tmp          # something for lane 2 to clean up
```

**3. Ask your AI for a lane.** Open Claude Code (or any coding agent) in
`swim-try` and paste:

> Run `swim guide` to learn swim. Write lane 1: a round that reports
> what's in this repo and how big it is. Lint it, and don't run it: I will.

With Claude Code, `swim skill` installs a skill that already knows swim,
and the first sentence can go. No AI handy? This is the kind of lane it
writes:

```sh
cat > lane.1.sh <<'EOF'
#!/usr/bin/env swim
# swim: syntax 4
# Round: Report what's in this repo

stage snapshot
snapshot "the files" ls -la "$SWIM_ROOT"

stage check
run "how big it is" du -sh "$SWIM_ROOT"
EOF
```

**4. Run it,** and read what came back:

```sh
swim run 1
swim log 1
```

Every step is in the log with its command, output, exit code and time,
marked PASS, FAIL or SKIP. The AI reads that, not your terminal.

**5. A guarded step, refused.** Ask your AI:

> Write lane 2: delete the .tmp files here, behind a guard.

```sh
cat > lane.2.sh <<'EOF'
#!/usr/bin/env swim
# swim: syntax 4
# Round: Delete the .tmp files
# Guards: TRY_ALLOW_DELETE  delete the .tmp files

stage snapshot
snapshot "the .tmp files" bash -c 'ls "$SWIM_ROOT"/*.tmp'

stage change
if guard TRY_ALLOW_DELETE "delete the .tmp files"; then
  run "delete them" bash -c 'rm -v "$SWIM_ROOT"/*.tmp'
fi
EOF
swim run 2 || echo "exit $?: nothing deleted"
ls *.tmp
```

On a terminal, swim asks `Y/N [N]`. Press Enter to say no. The guarded
step becomes a **dry run**, and the files are still there:

```
SKIP  delete the .tmp files (dry run: set TRY_ALLOW_DELETE=1 to approve)
```

**6. Ask your AI what happened:**

> Read `swim log 2`. What happened, and what would it take to finish?

It reads the log, not your screen. The snapshot found two .tmp files, the
delete was a dry run because nobody approved `TRY_ALLOW_DELETE`, and
`TRY_ALLOW_DELETE=1 swim run 2` would finish the job. That's your call:

```sh
TRY_ALLOW_DELETE=1 swim run 2
ls *.tmp 2>/dev/null || echo "the .tmp files are gone"
```
<!-- /quickstart -->

The AI wrote both lanes and explained the result. It never ran anything,
and the delete happened only when you said so. Real work keeps the same
split, except the lane needs your VPN, your SSO session and your prod
keys, and the AI never gets any of them.

Next, [swim in an hour](docs/quickstart.md) covers gates, dependencies,
the queue and a swimmer, and signed approvals that an edit voids.

## What swim protects against

You're handing a script you didn't type to your own credentials, so swim
makes sure what runs is what you looked at:

- **The AI never holds your access.** It writes lanes; only you, or a
  swimmer you started, runs them.
- **What you approved is what runs.** An approval binds the exact bytes:
  the script and every file it pins. Change any of it and the guarded step
  is a dry run again. The lane runs in a staged copy of exactly those
  files, so an edit mid-run never reaches it.
- **A lane can't approve itself.** Under `swim run` the launcher decides
  every guard, not the script.
- **Bypasses are recorded.** `FLAG=1`, `-y` and `swim watch --approved`
  skip the question, and each leaves its mark in the log.
- **Out of scope:** a bad script that you approve. A lane runs as you and
  can do anything you can. Review what you approve, and use scoped
  credentials for anything you can't take back.

The details, and the test behind each claim, are in the
[trust model](docs/trust-model.md).

### When you need proof

Every round and approval also goes into a signed, hash-linked record.
You don't have to use it, but when someone asks what ran in prod and who
approved it, `swim verify` answers:

<!-- tamper -->
```sh
# The AI wrote lane.1.sh: one guarded step that runs install.sh, which
# its header pins (# Includes: install.sh).
swim approve 1 --guards CERT_ALLOW_ROTATE    # you sign the script, install.sh, how it runs and the policy

echo 'echo DROP EVERYTHING' > install.sh     # anyone changes a file it uses after you signed
swim run 1 --no-prompt
#   SKIP  replace the live cert (dry run: ...signature approves another version of lane.1.sh
#         (approved c4d0cc7ede49, running 12a561d369b4); review the change ..., then:
#         swim approve 1 --guards CERT_ALLOW_ROTATE)

git checkout install.sh && swim run 1         # back to the bytes you signed
#   APPROVED  replace the live cert (approved by me (signed))
#   PASS  install the new cert

perl -pi -e 's/new cert/old cert/' .swim/logs/agent1.log   # someone edits the record afterwards
swim verify
#   BROKEN: entry #5: swim 1's log for run r-20261023T224837Z-a11f was changed after the round ended
#   NOT VERIFIED: consistent: 1 problem (BROKEN, above)
```
<!-- /tamper -->

The same record supports two-person approval rules (`lookup.yml`),
approvals signed from a teammate's machine, and running rounds in CI. See
[approve from anywhere](docs/usage.md#6-approve-from-anywhere-as-a-team)
and [chain of custody](docs/usage.md#chain-of-custody).

## FAQ

<details>
<summary><b>Why not give the AI the whole environment and let it run everything itself?</b></summary>

Because then everything it improvises runs with your authority. Where a
mistake is cheap and reversible (tests, a browser, local iteration), let
it act directly; swim only adds friction there. swim is for work where the
credentials or the consequences are yours: you see the whole plan as a
file before anything runs, and the AI never holds the keys.

</details>

<details>
<summary><b>Isn't it slower to hand scripts back and forth?</b></summary>

For a quick local loop, yes. For ops work it's usually faster: no session
waits on a long job, independent lanes run at once (`swim all --parallel
N`), and the queue keeps work moving while you're busy or away.

</details>

<details>
<summary><b>Does my team need to use it?</b></summary>

No. Lane scripts and swim's state are git-ignored by default, so swim
lives in your checkout. If teammates do pick it up, approvals and the
record work across machines.

</details>

<details>
<summary><b>How is this different from Ansible?</b></summary>

Ansible is the closest neighbor: an agentless CLI that runs from your
machine with your credentials. But it wants reusable, idempotent
playbooks, `--check` covers a whole play rather than one step, and its
output isn't written for an AI to read back. swim is for the one-off
script an AI wrote this morning. They work together: call
`ansible-playbook` from a guarded step, and the run gets swim's queue,
log and approval.

</details>

<details>
<summary><b>How is this different from Rundeck or AWX?</b></summary>

They're servers for curated runbooks: a team installs them, maintains
the jobs and manages access. swim has no server, and its rounds are
disposable: the AI writes a new one each time. If your team needs shared,
role-based runbooks, use those. If you need to run today's change from
your own shell, use swim.

</details>

<details>
<summary><b>Why not Airflow or Dagster?</b></summary>

They schedule recurring data pipelines on workers you deploy. swim's DAG
is today's cutover plan, run once, from your machine: `# After: 2` in a
lane script, no scheduler, no metadata database.

</details>

<details>
<summary><b>Why not Temporal?</b></summary>

Temporal makes application workflows durable: you build a product on it.
swim is something an operator runs. If the work should survive a crashed
worker and retry for days, that's Temporal's job, not swim's.

</details>

## Where next

- [Using swim](docs/usage.md): how it works, common usage patterns, and
  the reference for every feature, command, file and setting.
- [swim in an hour](docs/quickstart.md): a tested walk from a first round
  to guards, a dependency, a queue, a swimmer and signed approvals.
- [The docs](docs/README.md), in reading order: architecture, state
  machines, recovery, compatibility, frozen formats.
- [The trust model](docs/trust-model.md): what swim protects against, what
  it doesn't, and the test behind each claim.
- **AI agents:** run `swim guide`, the full guide for planners and
  workers, or `swim skill` to install the Claude skill.
- [The product brief](swimlane.md): why swim exists and what it must
  guarantee.
- **swim's own CI** is the `lane.*.sh` files at the root of this repo:
  checks first, then e2e, fault, security, fuzz, unit and race tests side
  by side, run by `swim ci` ([swim.yml](swim.yml) lists them).

## License

MIT. See [LICENSE](LICENSE).
