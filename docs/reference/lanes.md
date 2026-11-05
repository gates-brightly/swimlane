# Lanes

Writing a lane script. Start from `swim new N "<goal>"`.

## Forms

- **Short** (what `swim new` writes): first line `#!/usr/bin/env swim`,
  then the header and steps. `swim run` loads the library, calls
  `lane_init N` and ends the round when the script ends. `./lane.N.sh`
  runs it as `swim run N` does; `bash lane.N.sh` can't.
- **Explicit:** loads the library itself and calls `lane_init N` first and
  `summary` last, so it also runs with plain bash:
  `_swim_lib=$("${SWIM_BIN:-swim}" _lib) || exit 1; eval "$_swim_lib"`.

A lane's manifest pins which form it uses (its runner).

## Example

```bash
#!/usr/bin/env swim
# swim: syntax 4
# Round:   Cut over orders-api from SST to Terraform
# After:   1
# Guards:  ORDERS_ALLOW_SST_REMOVE  remove SST stack orders-prod (2026-10-09: tf owns it)
# Locks:   tf/orders
# Timeout: 30m

stage snapshot
snapshot "cfn template" aws cloudformation get-template --stack-name orders-prod
stage check
step  "tf plan" terraform -chdir=infra/orders plan -detailed-exitcode
gate "DeletionPolicy is Retain (deployed)" bash -c 'aws cloudformation get-template --stack-name orders-prod | grep -q Retain'
stage change
if guard ORDERS_ALLOW_SST_REMOVE "remove SST stack orders-prod (2026-10-09: tf owns it)"; then
  step "sst remove" npx sst remove --stage prod
fi
stage verify
step "re-plan is a no-op" terraform -chdir=infra/orders plan -detailed-exitcode
```

## Functions

| | |
|---|---|
| `lane_init N` | Explicit form only, first: cd to the lane's workspace (its pinned copy under `swim run`), open the round, export `SWIM_ROUND_ID`, `SWIM_DEADLINE`, `SWIM_TIMEOUT`. Refuses a lane already running. |
| `stage NAME` | Start a [stage](#stages). Steps before the first are setup. |
| `step [opts] "<label>" cmd [args]` | A step: PASS/FAIL. A failure doesn't stop the round. (`run`, its old name, works for one more release; `swim lint --fix` rewrites it.) |
| `gate [opts] "<label>" cmd [args]` | A step whose failure (after the last attempt) stops the round. |
| `snapshot [opts] "<label>" cmd [args]` | A read-only step whose output is also saved under `.swim/snapshots/`. |
| `guard FLAG "<action - date, reason>"` | True if FLAG is approved for this run, else logs SKIP (a dry run). See [guards](guards.md). |
| `confirm "<question>" [answer]` | The operator must type the answer (default `yes`); anything else, or no stdin, stops the round. |
| `last_failed` / `any_failed` | True if the previous step / any step this round failed. |
| `reached STAGE` | True if the round started STAGE or a later one. |
| `source FILE` | Under `swim run`, a repo file is read from the stage, and only if `# Includes:` lists it. |
| `summary` | Explicit form only, last: log the summary, record the final state, exit non-zero if a step failed. |

**Step options**, before the label:

| | |
|---|---|
| `--timeout D` | Stop the step after D (`30s`, `5m`): FAIL, exit 124. `0` turns off `Step-Timeout:` for this step. |
| `--retry N` | Up to N more tries on a non-zero exit. Only on read-only or idempotent steps. |
| `--backoff D` | Wait before the first retry (default 2s), doubling to 5m, ±20% jitter. |
| `--retry-on CODES` | Retry only on these exit codes (`1,255`). |
| `--no-retry-timeout` | Don't retry an attempt the limit stopped. |
| `--soft` | In a `Fail: hard` round, this step's failure doesn't end it. |
| `--key K` / `--target T` | The step's name across rounds / what it talks to (default: `# Targets:`), both logged. |
| `--expect-fail[=CODES]` | A negative test: PASS on a non-zero exit (one of CODES), FAIL on 0. Not with `--retry`. |

The round's `Timeout:` always wins. A step runs in its own process group,
and a limit stops all of it (SIGTERM, SIGKILL after 5s), except a step
reading the terminal, which stays in the lane's group and is stopped alone.

## Shared helpers

Behaviour many lanes repeat goes in a module the lanes include, not in each
lane. swim ships modules of bash functions, one per namespace, every
function named `<module>_<name>` (as Terraform names resources by
provider). `swim helpers` lists them, one line each; `swim helpers gh`
shows a module's functions, their arguments and what each does; `swim
helpers add` writes the ones you use into the repo, to `lib/swim/`:

```bash
swim helpers add aws gh os     # lib/swim/aws.sh, gh.sh, os.sh; commit them
```

```bash
# Includes: lib/swim/aws.sh lib/swim/gh.sh lib/swim/os.sh
source lib/swim/aws.sh
source lib/swim/gh.sh
source lib/swim/os.sh
stage check
os_mac_gate             # gate: this is the operator's Mac
aws_gate --profile sbx1 123456789012   # gate: sbx1's AWS session is active, and is that account
gh_pr_fetch 482         # gate: fetch PR 482 (remote: origin) to $GH_PR_REF, snapshot its head
```

| | |
|---|---|
| `aws_gate [--profile PROFILE] [ACCOUNT]` | A gate that `aws sts get-caller-identity` answers (under PROFILE, else `AWS_PROFILE` if set), and with ACCOUNT, that it's that account. The log shows the identity; an expired SSO session names the `aws sso login` that renews it. |
| `gh_pr_fetch N [REMOTE]` | A gate that fetches pull request N's head from REMOTE (default `origin`) into `refs/remotes/REMOTE/pr/N`, with retries; snapshots the commit and sets `GH_PR_REF`. |
| `gh_actions` / `gh_actions_gate` | True on a GitHub Actions runner (`GITHUB_ACTIONS=true`), for `if`; the gate stops the round anywhere else and logs the runner, repository, run and commit. |
| `gh_auth_gate [LOGIN]` | A gate that `gh` is signed in, and with LOGIN, as that user. |
| `gh_repo [REMOTE]` | Sets `GH_REPO` (owner/name, as `gh` reads it) from REMOTE's URL (default `origin`); stops the round if it isn't GitHub's. The `gh_pr_*` gates set it when unset. |
| `gh_pr_state_gate N STATE...` | A gate that PR N is OPEN, MERGED, CLOSED or DRAFT (one of those given); logs its state, head and review decision. |
| `gh_pr_head_gate N SHA` | A gate that PR N's head is still SHA (a prefix will do): what was reviewed is what it holds. |
| `git_fetch_gate REMOTE REF...` | A gate that fetches REFs from REMOTE, with retries. |
| `git_worktree PATH BRANCH [BASE]` | A gate that PATH (under the repo) is a worktree on a new BRANCH from BASE (default `origin/main`); rerunnable; sets `GIT_WT`. |
| `git_pushed_gate BRANCH [REMOTE]` | A gate that REMOTE (default `origin`) has BRANCH at the local branch's commit. |
| `git_authors_gate "NAME <EMAIL>" RANGE [PATH]` | A gate that every commit in RANGE was authored and committed by that identity; lists any that weren't. |
| `docker_gate` | A gate that the Docker daemon answers. |
| `docker_compose_snapshot PROJECT [WHEN]` | Snapshot a compose project's containers, running or not (`before`, `after`). |
| `tool_gate CMD [VERSION \| VERSION+]` | A gate that CMD is on PATH, and with VERSION that `CMD --version` reports it, or `CMD version` when that fails (`node 20` matches v20.11.1, `go 1.27` matches go1.27.1); `VERSION+` is that or later (`node 20+` matches v22.3.0). |
| `os_mac` / `os_linux` | True on macOS / Linux (`uname -s`), for `if`. |
| `os_mac_gate` / `os_linux_gate` | A gate that this is macOS / Linux; logs `uname -srm`. |

The plain checks (`gh_actions`, `os_mac`, `os_linux`) log nothing: put
what depends on them in steps, or use the gate when the lane must run
there.

Modules: `aws`, `docker`, `gh`, `git`, `os`, `tool`. A copy is plain bash
in the repo: `# Includes:` pins it with the lane, so approving the lane
approves the helpers as they are, and an update is a diff you review. Its
first line stamps the module, the swim it came from and a hash of the
rest: `swim helpers add` again updates a copy, and `swim lint` says when
an included copy was edited since (`helpers`, a warning), or this swim
ships a different version (info). A repo's own module (`lith_` for its dev
stack, say) is a file like these without the stamp. Helpers change at most the repo's own
git state (refs, a new worktree), and run everything through `step`,
`gate` or `snapshot`, so the log has it. A gate's function is one line of
bash that runs its check as `swim _helper FUNC ARGS...`: the check is
swim's own Go, tested with swim and pinned by its version as `gate` is,
and what a lane approves in the copy is which check runs, with what. The
commands a check runs (`aws`, `gh`, `git`) aren't on the command line, so
`blocked_commands` doesn't see them; none of them change anything outside
the repo's git state. A destructive step stays in the
lane behind its own guard.

A function that changes something says so in its comment, with a
`# Changes: WHAT` line (`git_worktree`: a new branch and worktree in the
repo); `swim helpers MODULE` shows it. Lint holds a lane's call of one to
the rules for a destructive command: outside a guard block, it warns
(`destructive`). Lint leaves swim's own modules, as shipped, to swim's
tests; any other module a lane sources (a repo's own, an edited copy) it
reads on its own file, as it reads a lane, for destructive and blocked
commands, bash 3.2, BSD tools, how steps are written and secrets, leaving
alone the body of a function that declares `# Changes:`. A disable comment
in the module covers its own lines; one in the lane covers a module's only
with `-file`.

In the log, a helper's step shows the call, not the command it runs, and
its `helper:` line names the function, the copy and the swim it came from:

```
  FAIL  go 1.27 (exit 1)                        0.0s  21:35:51
        $ tool_gate go 1.27
        | /usr/local/go/bin/go: go version go1.26.3 linux/arm64
        | want go 1.27, got 1.26.3
        helper: tool_gate, lib/swim/tool.sh, tool from swim 0.20261106.1
```

A helper that fails ends with one line saying what's wrong and the
command that fixes it; that line is the step's `why:`, so the summary,
`swim status` and `.swim.log` carry it:

```
  FAIL  AWS session active (exit 1): no AWS session (profile sbx1): aws sso login --profile sbx1
```

The call stands for the command only when the copy is this swim's module
as shipped, byte for byte, so the command is the one in that module. An
edited copy, another swim's version or a repo's own module shows the
command in full, and its `helper:` line says why. Modules pass the call
with `step`, `gate` and `snapshot`'s `--helper CALL`, which is theirs:
lint refuses it in a lane.

`swim helpers stats` reads every round in the lane logs, archives too:
each helper's calls and failures, most failures first, with the last
failure's why; and the checks lanes wrote by hand in 3 or more rounds of
2 or more round goals (numbers in a label don't count, so `AWS session is
sbx1` and `sbx2` are one check). Those are the candidates for a helper.

## Header

`# Key: value` lines at the top (at most one space after `#`). Only
`Round` is required; empty means the default. Other keys (`# Ticket:
OPS-42`) are kept and shown in the log; a value swim can't use is a WARN.

| | |
|---|---|
| `# swim: syntax 4` | The [syntax](compatibility.md#the-syntax-version). |
| `# Round:` | The goal. A script without one isn't run. |
| `# Round-ID:` | The round id, written by `swim new`; keep it. (An older script's `# Job:` reads the same; `swim lint --fix` rewrites it.) |
| `# Task:` | A name for the lane (`swim run checks`). |
| `# After:` | Lanes, tasks or round ids that must pass first. |
| `# Owner:` / `# Created:` | Who wrote it / when. |
| `# Guards: FLAG  <what it approves>` | One per flag: the only guards it can have approved. `# Guards: * <why>` lets it name flags at run time. `swim lint --fix` adds them. |
| `# Locks:` | Resources it needs to itself (`orders-table, tf/orders`). |
| `# Step-Timeout:` / `# Timeout:` | Default limit per step / limit for the round. |
| `# Targets:` | What its steps talk to by default. |
| `# Fail: soft\|hard` | `hard`: the first failed step ends the round. |
| `# Includes:` | Repo files that are part of the round (sourced files, local modules): pinned and staged; the only repo files it may read as code. |
| `# Workspace: live` | Run in the checkout itself, not a pinned copy (for tools that read the whole tree, like `go test`); lint warns (`workspace-live`). |

## Stages

| | |
|---|---|
| `snapshot` | Read-only capture of current state. |
| `check` | Checks and gates; nothing changes yet. |
| `change` | The change, destructive steps behind guards. |
| `verify` | Prove it worked (a re-plan is a no-op). |

In that order; leave out what you don't need. Out of order, twice, or
`change` without snapshot and check before it: a WARN. An unknown name
stops the round. Each stage's result (PASS, FAIL, SKIP, none) is in the END
block and `status.yml`.

## Rules

- No `set -e`: failed checks keep going; only gates stop the round. A
  command outside a step that fails is logged `WARN outside a step` and
  changes nothing: put what matters in a step.
- Steps take argv, not a shell string: `step "count" bash -c 'scan | jq .Count'`.
- Load your toolchain (nvm, asdf, pyenv) yourself: non-interactive shells
  don't.
- Snapshot first. Every destructive step behind its own guard, after a gate
  that tells "ours" (tagged) from "theirs".
- Rerunnable: steps idempotent or detecting their own prior success.
- End with a verification step that prints the evidence (counts, ids, "No
  changes"): it's the part of a passing round anyone reads.
- Check the target first: a gate that stops on the wrong account. For
  Terraform, `snapshot "plan" terraform plan -out tfplan` in check, `terraform
  apply tfplan` inside the guard, so what's applied is what was seen.
- Many similar targets: one step each, fed without a pipe (`while read t; do
  step "drain $t" ...; done < <(list-targets)`); a piped loop runs in a
  subshell and loses its failures (lint: `step-subshell`).
- Keep output streaming: not `out=$(cmd); echo "$out"`, but `bash -o
  pipefail -c 'cmd | awk "{print} /bad/{b=1} END{exit b}"'`. Python CLIs
  buffer when piped: `PYTHONUNBUFFERED=1`.
- A lane runs in a pinned copy holding the script and what `# Includes:`
  pins; `$SWIM_ROOT` is the repo, for what the round acts on.
- Header values take no trailing `# comment`; explain on lines of their own.
- Write for the operator's machine, not yours: often macOS, so bash 3.2 (no
  associative arrays, `mapfile`, `${x,,}`) and BSD tools (`sed -i.bak`; no
  `date -d`, `grep -P`, `stat -c`, `find -printf` or `timeout`: use
  `--timeout`). `swim lint` warns about both (`bash4`, `gnu-only`), and
  `swim doctor` on the operator's machine says which of them it can't run.
- No LLM calls that decide what the round does (branching, gating,
  retries): the script the operator approved decides. One may produce an
  artifact nothing reads back.

## Lint

`swim lint` reports errors, warnings and info with `file:line`, a fix and a
`[code]`; exit 1 on an error (`--strict`: on a warning). `swim run` refuses
`set -e` and a missing or wrong `lane_init`. Accept a finding with a
reason:

```bash
rm -rf "$TMP"   # swim:lint-ignore destructive scratch dir this round made
# swim:lint-ignore bash4 runs only on the Linux CI box          (the next line)
# swim:lint-ignore-file cross-lane reads lane 1's output        (the file)
```
