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

It is the playbook: the two machines, what you run and never run, how to
hand a round over, workers, several tasks at once. Follow it.

## The loop

1. **Catch up:** `swim`, `swim status`, then `swim log --since 1h` (or the
   last run id you read): one line per action since.
2. **Write** `swim new <task> "<goal>"` (the lowest free lane), then the steps (`swim guide lanes`):

   ```bash
   # Guards: ORDERS_ALLOW_DELETE  delete CloudFormation stack orders (account 123456789012)
   #   why: terraform owns it since 2026-11-05
   stage snapshot
   snapshot "the stack" aws cloudformation describe-stacks --stack-name orders
   stage check
   gate "right account" bash -c '[ "$(aws sts get-caller-identity --query Account --output text)" = 123456789012 ]'
   stage change
   if guard ORDERS_ALLOW_DELETE; then
     step "delete" aws cloudformation delete-stack --stack-name orders
   fi
   stage verify
   step "it's gone" aws cloudformation describe-stacks --stack-name orders --query 'Stacks[0].StackStatus'
   ```

   `step` records a command; a failed `gate` stops the round; `snapshot`
   also saves its output; `guard` is a dry run until the operator approves.
   The operator is asked the guard's two header lines: what it does (the
   action and exact target, one line) and, under it, `why:` (the reason,
   with its date). Write both so they can say yes without reading the
   script.

   **Check `swim helpers` before writing a check by hand.** It lists the
   helper modules (`aws`, `gh`, `git`, `docker`, `tool`, `os`), one line
   each; `swim helpers gh` shows each function's arguments and what it
   does. Use one when it fits (`aws_gate 123456789012` rather than your own
   `sts get-caller-identity` gate): include and source the module the way
   `swim helpers gh` says. If the repo has no copy yet, `swim helpers add
   gh` writes one to `lib/swim/`; tell the operator to commit it. Never edit
   a copy (lint warns): a check the modules lack goes in the lane, and one
   lanes keep repeating is worth proposing as a module (`swim helpers
   stats` lists the checks lanes keep writing by hand). In a repo's own
   module, a function that changes something says so in its comment
   (`# Changes: drops the dev database`): lint then wants its calls guarded.
3. **Check:** `swim lint N` until clean, then `swim status N`.
4. **Hand over:** `swim queue N` (it runs and approves nothing; it prints
   the batch id), or give the operator `swim run N`. Never run it, and
   never approve.
5. **Wait, don't poll:** `swim wait <batch>` blocks until the batch ends
   and exits with its result (0 passed, 3 dry run, 1 otherwise).
6. **Read:** `swim status N`. A failed step comes with its why, one line:
   `FAIL  go test (exit 1): --- FAIL: TestX: x_test.go:41: got 1`. When
   that isn't enough, `swim log N`. `passed` means exit 0: read the verify
   stage's output. A dry run changed nothing.
7. **Close out:** once you've read what shows it worked, `swim archive N
   "<why it's done>" --as <what-happened>`: the log is kept, the lane's
   script goes, and the slot is free again. Archive a failed round you're
   abandoning too, with a name that says so (`--as orders-apply-failed`).

## Keep the repo clean

- **Lane numbers:** `swim new <task>` takes the lowest free lane; with a
  number, use the lowest free one, never the highest plus one.
- **Your own files** (notes, drafts, task lists, PR descriptions) go in
  your scratchpad or `swim note`, never in the repo.
- **A lane's output** goes through `snapshot` (kept under
  `.swim/snapshots/`), never `> file` in the working tree.

Then the topic for the job at hand, `swim guide <topic>`:

| Before you... | Topic |
|---|---|
| write or edit a lane | `lanes`, `guards`, `safety`; shared helper modules (`aws_`, `docker_`, `gh_`, `git_`, `os_`, `tool_`; `swim helpers`) are under "Shared helpers" in `lanes` |
| write a lane in Python, TS or Go | `other-languages` |
| read results | `logs` |
| something looks wrong | `recovery` |
| anything else | `swim guide` lists every topic |

`swim <command> --help` has one command's detail.
