# Writing a lane: the rules that matter most

Read before writing or editing a lane script, with `swim guide` sections
WRITING A LANE SCRIPT, SAFETY RULES and SYNTAX. The annotated example is
[lane-recipe.md](lane-recipe.md).

- **The short form:** the script is its header and its steps (`swim new`
  writes it). `./lane.N.sh` runs it as `swim run N` does; `bash lane.N.sh`
  can't. Only write the explicit form (library line, `lane_init N`,
  `summary`) when the lane must also run with bash directly.
- **Stages in order:** `snapshot` (read-only capture), `check` (checks and
  `gate`s), `change` (only behind `guard`), `verify` (prove it worked).
  Make `verify` print the evidence a reader needs (counts, ids, "No
  changes"): it's the part of a passing round anyone reads.
- **A guard's description is the operator's summary of what they're
  approving:** swim shows it when they approve, in `swim status` and in the
  chain, and it's often all they read. Write one
  line that says the action (the verb and the command's effect), the exact
  target (resource, environment, account), the scope (how many, how much,
  from the snapshot or check that counted it), whether it can be undone
  and how, then the date and the reason:
  `guard USERS_ALLOW_PURGE "delete the 3 @test.example rows from prod
  users (snapshot keeps them; restore with users-restore.sh) 2026-10-29:
  load-test cleanup"`, not `"cleanup"` or `"run the migration"`. The same
  text goes in `# Guards:`. If one line can't say it, the guard covers too
  much: split it.
- **Every destructive or irreversible step goes behind its own `guard
  FLAG "<action, date, reason>"`.** Declare it in the header (`# Guards:
  FLAG  <what it approves>`): swim reads a lane's guards and includes
  from its header only, and refuses one it doesn't declare (a guard
  dry-runs; an unlisted `source` stops the round). `swim lint --fix` adds
  missing header lines.
  With no approval it's a dry run, which is the safe default. Never default
  a flag on, and never set one in the script: swim refuses that.
- **Mark steps that mustn't run twice `--checkpoint`** (`run --checkpoint
  --key create-bucket "create the bucket" aws s3 mb ...`): slow or
  one-time changes whose effect lives outside the repo. When the round
  fails later and is fixed, its next run keeps them (KEPT) and resumes.
  Never on a check or snapshot. Clearing them (`swim run --fresh`,
  `swim checkpoint rm`) is the operator's call.
- **When the operator knows which way to go and you don't, write a
  decision, not a guess.** `decide NAME "<question>" "<choice 1>" "<choice
  2>" ...` (2 to 9 choices, declared in `# Decisions: NAME  <what it
  decides>`) on its own line, then `case $DECISION in 1) ... ;; esac`.
  Write every path; the operator picks one at run time. Unanswered, the
  round ends there SKIP. A decision steers; a destructive step on a branch
  still goes behind `guard`.
- **Gates fail closed.** An empty or unknown answer stops the round before
  any change.
- **Read the deployed state.** Never assume it from code.
- **Rerunnable:** tag what you create with `$SWIM_JOB`, so a rerun tells
  "ours" from "theirs".
- **Declare ordering in the header,** not in prose: `# After: 2 3`,
  `# Locks: name`, `# Includes: lib/x.sh` for anything you `source`.
- **The lane runs in a staged copy** holding only the script and what
  `# Includes:` pins, so what runs is what was approved. A repo file it
  reads but doesn't pin isn't there: pin it, or (rarely, and the approval
  says so) add `# Workspace: live`. Write the lane's own outputs to
  `$SWIM_OUT`; `$SWIM_ROOT` is the repo root, for the files the round acts on.
- **bash 3.2** (macOS): no associative arrays, `mapfile` or `${x,,}`. **No
  `set -e`.** A failed `run` doesn't stop the round; a failed `gate` does.
- **Write for the operator's machine, not yours.** The lane runs where the
  operator is (often macOS), not in your sandbox, so a command you tried
  here proves nothing there. On macOS the tools are BSD's: `sed -i.bak`
  (both kinds take it; bare `sed -i` is GNU's), no `date -d`, `grep -P`,
  `stat -c` or `find -printf`, and no `timeout` (use the step's
  `--timeout`). Name paths from `$SWIM_ROOT` or `$SWIM_OUT`, never `~` or
  your own home directory.
- **git and gh are allowed** (commit, push, pull, `gh pr create`, `gh api`).
  A step that changes a shared remote (a push, a PR, a merge) goes behind
  its own guard, like any change outside the repo. swim blocks only what
  the repo lists in `blocked_commands` (`swim config` shows them), on the
  text, so not even an echo of one.
- **Secrets:** a lane gets credentials from its environment (the
  operator's, or `.env.swim`). Never print values, only lengths. Never list
  a secret in `header_env`.
- **Check the target before changing it.** Start a change against cloud
  resources with a `gate` that stops on the wrong account (`gate "account"
  -- test "$(aws sts get-caller-identity --query Account --output text)" =
  "$ACCOUNT"`). For Terraform: `snapshot "plan" -- terraform plan -out
  tfplan` in the check stage and `terraform apply tfplan` inside the guard,
  so what's applied is the plan the operator saw.
- **Keep output streaming,** so the operator and the live view see
  progress. Don't capture a long command with `out=$(cmd); echo "$out"`.
  Check its output while it streams instead:
  `bash -o pipefail -c 'cmd | awk "{print} /bad/{b=1} END{exit b}"'`.
  Python tools buffer when piped: set `PYTHONUNBUFFERED=1` for them.
- **No Claude, LLM or agent calls in a lane that decide what the round
  does** (branching, gating, retries, approvals). Decisions are made in the
  script you wrote, which the operator approved. An LLM step may only
  produce an artifact (a report, a note) that nothing in swim reads back.
- **Don't edit a lane while it may be running,** because bash reads scripts
  incrementally. `swim status` shows what's running.

## Talking about safety

Be accurate about what swim guarantees. Approvals, signatures and the chain
prove **who decided what** and that **the approved bytes ran**. They don't
make a script safe: an approved script runs with the operator's access. A
guard is a bash function in the lane, so it's advisory. Never
tell the user a change is safe because swim will ask first. The full model
is `docs/trust-model.md` in the swimlane repo.
