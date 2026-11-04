---
name: swim
description: Plan and write operational work for swim (swimlane), the operator-in-the-loop runner. Claude writes lane scripts, a person runs them, and Claude reads the logs back. Use when a repo has lane.N.sh / swim.yml / .swim/, when the user mentions swim, lanes, rounds, guards, approvals, the queue or `swim watch`, or when operational work (infra changes, migrations, cutovers, prod diagnostics) has to run somewhere Claude can't reach (production, a VPN, the user's credentials).
---

# swim

**You write the round, the operator runs it, the log reports.** You plan
and write lane scripts; a person (the *operator*) runs them with their own
access; you read the results back from files and decide the next round.
Your context is for the task, not for watching runs: dispatch, work on
something else, and read only what you need when the result is in.

## Is swim here?

```sh
command -v swim && swim --version
ls lane.* swim.yml .swim 2>/dev/null
```

- **No `swim`:** say so, and stop unless the user wants it. Don't install
  it yourself without being asked (`swim skill` and the README say how).
- **No lane files, swim.yml or .swim:** the repo doesn't use swim yet.
  Don't run `swim init` yourself (it edits `.gitignore` and the user's
  config); propose it when the work has to run where you can't.
- **The binary wins.** This skill matches a swim whose `swim new` writes
  the short form (`#!/usr/bin/env swim`). If it writes something else, or
  a command here is unknown, follow the binary and tell the user once.

## Two machines

You and the operator are usually on different machines. Assume this
unless the project says otherwise (its CLAUDE.md, your instructions):

- **You:** a sandbox that sees only this repo. No cloud credentials, VPN,
  SSH keys or the operator's home directory: don't probe for them or work
  around their absence.
- **The operator:** their own machine, often another OS (macOS: bash 3.2,
  BSD `sed`, `date`, `grep`), with their access. Lanes run there, with
  that access, beyond this repo. The sandbox limits you, not what you
  write.
- **Only the repo is shared.** `swim status`, `queue`, `log` and `report`
  read it, so they show the operator's runs. `swim env`, `doctor` and
  `config` describe your machine, not theirs: don't judge the operator's
  environment by them.
- **Need a fact from their side?** Write a lane of snapshot steps (it
  reads, changes nothing) and queue it, rather than asking the operator to
  paste output. A command that works in your sandbox hasn't run on theirs.
- **Never write files their machine runs on its own:** `.git/hooks/`,
  `.envrc`, editor tasks. Nothing approves them: they'd run with the
  operator's access the moment they open the repo or commit.

## Run freely

These only read or check: `swim` alone (what needs the operator, what's
running, what's ready), `swim status`, `queue` / `queue show`, `log`,
`report` (from your own earmark), `lint`, `history`, `verify`, `env`,
`keys`, `doctor` (without `--fix`), `changelog`, `guide`, `--help`, and
`wait` (blocks until batches end; changes nothing). Add `--yaml` only when
you'll parse the output.

Bookkeeping on your own rounds: `swim new`, `archive`, `stub`, `note`, and
`claim` (a lane, a lane for a worker with `--session`, or a shared
resource by name). They touch only lane files, claims and logs, and refuse
a running lane.

## Hand over

**Queue a ready round** (`swim queue N ...`), guards and all, without
asking first. Queueing runs and approves nothing: the swimmer runs the
batch when its lanes are free and asks the operator for every guard
nothing else approved; one they decline is a dry run. Don't ask "shall I
queue it?" and don't hold a round back for its guards. With no swimmer up
(`swim queue` says), give the operator the exact `swim run <job> ...`
command instead, pinned to job ids, with nothing that approves.

Before handing over, read `swim lint` and `swim status` for the lanes and
**flag every item with meaningful risk**, each on its own line marked ⚠:
a guarded step that changes or destroys something outside the repo (name
the guard and the action), production or shared targets, a setup lane the
batch pulls in, anything that can't be undone. Give a **What you're
approving** block for each guard and decision
([reference/handoff.md](reference/handoff.md)). Never ask for an approval
without it.

**How the operator approves is theirs.** Don't look into it, name it,
suggest a way, or plan around it. Write every guarded step as if it will
be asked, and never skip, split, reword or hold back a step because of
how it might be approved.

## Never do

- **Run rounds:** `swim run`, `swim all`, `swim N`, `./lane.N.sh`, `swim
  watch`, `swim ci`. The operator's environment is the point. If the user
  explicitly asks, run only lanes with no guards that change nothing
  outside the repo (lint, tests, local builds), and say so.
- **Approve or decide anything,** in any form. Never set an environment
  variable named after a guard or decision.
- `swim interrupt`, `swim keys trust`, `swim chain ...`, `swim doctor
  --fix`, `swim config` or config edits.
- **Edit by hand:** `.swim/`, `.swim.log`, `approvals/`, `lookup.yml`,
  `trust.yml`, `~/.config/swim/`. Not even to fix one: give the operator
  the exact lines or command ([reference/triage.md](reference/triage.md)).
- **Tell the user a change is safe because swim will ask first.**
  Approvals prove who decided what; an approved script still runs with the
  operator's access.

## The loop

1. **Catch up, cheapest first:** `swim` (what needs the operator), then
   `swim status`, `swim queue`, `swim log | tail -20`. Then use your own earmark (one name per session and worker,
   or one reader moves another's place): `swim report --earmark --name
   NAME` prints what's new since it (failures in full) and moves it.
   **Never report from an earmark you haven't set** (`swim report
   --earmarks` lists them): the first report is every current round. Set
   yours without reading: `swim report --earmark --name NAME >/dev/null`.
   After a big batch, read per lane (step 6) instead. Don't redo or
   contradict what the record says without asking.
2. **Pick lanes:** idle ones or unused numbers (1–99). Never a lane that's
   running, holds a round someone may be about to run, or that the repo
   commits (its CI). Ask when unclear. With subagents, one lane per worker,
   claimed for it before it starts (`swim claim N --session NAME`;
   [reference/workers.md](reference/workers.md)).
3. **Write:** `swim new N "<goal>"`, then the steps, in the short form, in
   bash unless the work needs another language. Read
   [reference/lane-rules.md](reference/lane-rules.md) first.
4. **Check:** `swim lint N` until clean (`swim run` refuses any lint
   error), then `swim status N`: what it waits on, and its guards.
5. **Dispatch:** hand it over (above), then go on to the next task. Never
   poll with `swim run`, and never `swim queue ... --wait` while you have
   other work: it blocks until that batch ends. When there's nothing else
   to do, `swim wait --any '#41' '#42'` returns as the first of your
   batches ends. Several tasks at once:
   [reference/streams.md](reference/streams.md).
6. **Read results** when the batch is done (`swim queue` says so):
   `swim status N` first. **FAIL:** `swim log N`, the failed step and the
   ones just before it. **PASS only means exit 0:** read the evidence (the
   verify stage, `swim log N | sed -n '/^-- stage verify/,$p'`). **SKIP**
   (`dry run`): a guard wasn't approved, nothing changed; never a result.
7. **Close out:** record results where the project tracks them, `swim note
   "<finding>"` for the next session, then `swim archive N --as
   <what-happened>` and `swim stub N "<why idle>"`. Out-of-scope findings
   go on a follow-up list. Rounds that build on each other:
   [reference/campaigns.md](reference/campaigns.md).

Something looks wrong (stuck `running`, a round that stopped mid-change,
an orphaned batch, `swim verify` saying no):
[reference/triage.md](reference/triage.md), read-only first.

## Everything else is in the guide

The installed binary's guide is the source of truth. Read one section
instead of all of it:

```sh
swim guide | awk '/^PLANNER WORKFLOW$/{p=1;print;next} p&&/^[A-Z][A-Z ]+($|  )/{exit} p'
```

| Before you... | Read |
|---|---|
| plan a round | `PLANNER WORKFLOW`, `CONCEPTS`; `GOING FURTHER` for more |
| write one lane as a subagent | `WORKER RULES` |
| write or edit a lane script | `WRITING A LANE SCRIPT`, `SAFETY RULES`, `SYNTAX`, and [reference/lane-rules.md](reference/lane-rules.md) |
| write a lane in Python, TS or Go | `OTHER LANGUAGES` |
| read results | `READING RESULTS` |

`swim <command> --help` gives one command's detail.
