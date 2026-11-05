---
name: swim
description: Plan and write operational work for swim (swimlane), the operator-in-the-loop runner. Claude writes lane scripts, a person runs them, and Claude reads the logs back. Use when a repo has lane.N.sh / .swim/, when the user mentions swim, lanes, rounds, guards, approvals, the queue or `swim watch`, or when operational work (infra changes, migrations, cutovers, prod diagnostics) has to run somewhere Claude can't reach (production, a VPN, the user's credentials).
---

# swim

**You write the round, the operator runs it, the log reports.** You never
run a round or approve anything.

## Is swim here?

```sh
command -v swim && swim --version
ls lane.* .swim 2>/dev/null
```

- **No `swim`:** say so; don't install it unless asked (`swim skill`
  says how).
- **No lane files or .swim:** the repo doesn't use swim yet.
  Propose `swim init` when work has to run where you can't; don't run it.
- **The binary wins:** if a command here is unknown, follow the binary and
  tell the user once.

## Before your first swim command

```sh
swim guide agents
```

It is the playbook: the two machines, what you run and never run, the
loop, how to hand a round over, workers, several tasks at once. Follow it.

Then read the topic for the job at hand, `swim guide <topic>`:

| Before you... | Topic |
|---|---|
| write or edit a lane | `lanes`, `guards`, `safety` |
| write a lane in Python, TS or Go | `other-languages` |
| read results | `logs` |
| something looks wrong | `recovery` |
| anything else | `swim guide` lists every topic |

`swim <command> --help` has one command's detail.

Waiting on work you handed over: `swim queue N` prints the batch's id;
`swim wait <batch>` blocks until it ends and exits with its result (0
passed, 3 dry run, 1 otherwise). It is the only command that waits;
never poll.
