# swim

**Sane AI ops.**

Run AI-written operational changes safely, with a human in the loop and a
provable record of what happened.

An AI session (the *planner*) and its subagents (the *workers*) write
operational work as small scripts (bash, Python, TypeScript, or any
program), one per numbered *lane*. A person
runs them with `swim`, in places the AI can't reach: a VPN-only account,
their own credentials, production. Every step, result and approval lands in
plain-text logs the AI reads back to decide what to do next.

> The session writes. The operator runs. The log reports.

```
swim · myrepo · main@64a0e07 · 0:04
 swim 1  PASS 0:01                  Diamond root: fetch pages              51c7cf2e
 swim 2  PASS 0:03                  Child A: convert to Markdown           d7af1473
 swim 3  / running 0:04             Child B: link stats                    0acffc31
 swim 4  / waiting on swim 3        Join: build report                     db8e71f9
──────────────────────────────────────────────────────────────────────────────────
[3] ==> compute stats.json
[3] $ e2etool stats
[3] PASS  compute stats.json  0.1s
```

What you get:

- **Parallel lanes with dependencies:** independent rounds run at once, and
  dependent ones wait for the rounds they depend on.
- **Changes are dry runs until a person approves them:** destructive steps
  sit behind guards. You approve at the terminal, with a flag, or with a
  signed approval from a teammate somewhere else.
- **Logs both people and agents can read:** one plain-text format, readable
  with `cat` and parseable by an agent.
- **A tamper-evident record:** every approval and every round is signed and
  hash-linked, so you can prove what ran and who approved it.
- **The same rounds from a terminal, a queue, CI, or many machines,**
  watched from one dashboard.

The product brief is in [`swimlane.md`](swimlane.md). **AI agents** should
start with `swim --help`: it is the full guide for planners and workers.

### Install

swim is one Go binary (Go 1.27 or newer to build):

```sh
go install github.com/gates-brightly/swimlane/cmd/swim@latest
export PATH="$(go env GOPATH)/bin:$PATH"      # if ~/go/bin isn't on your PATH yet
swim --version                                # e.g. swim 7.20261013
```

More install options are under [Install and versions](#install-and-versions).

---

## Common usage patterns

Each pattern builds on the ones before it. Start with the first; reach for
the others when you need them.

### 1. Run a round yourself

```sh
cd your-repo
swim init                                  # config entry, .gitignore block, .swim/status.yml
swim new 1 "Cut over orders-api"           # writes lane.1.sh from a template
$EDITOR lane.1.sh                          # fill in the steps (or have an AI write them)
swim plan                                  # dry run: what would run, wait or be skipped
swim all                                   # run every pending lane, with a live view
swim log 1                                 # what happened, step by step
swim archive 1 && swim stub 1 "orders done"   # keep the log, free the lane for the next round
```

### 2. Let an AI plan, workers write, and you run

The planner splits the work into lanes and gives each worker one lane. Each
worker writes only its own `lane.N.sh`. You run `swim all` and the agents
read the results back:

```sh
swim status                 # what every lane is doing (or: cat .swim/status.yml)
swim log 2                  # a lane's log; swim log <job-id> works too
swim log                    # .swim.log: one line per top-level action, for catching up
```

The conventions agents follow live in `swim --help`: which lanes to use,
never editing a running lane, gates before changes, and how to read results.

### 3. Run a DAG in parallel

A round declares what it waits for, and swim schedules it:

```bash
# After: 2 3        # in lane.4.sh: start once swim 2 and swim 3 have passed
```

```sh
swim plan           # the dependency tree, Terraform style
swim all --parallel 8
swim timeline       # who waited on whom, and the longest chain
```

### 4. Make a guarded change

Destructive steps sit behind a `guard`. With the flag unset, swim asks the
person at the terminal; with no one to ask, the step is a dry run:

```bash
if guard ORDERS_ALLOW_SST_REMOVE "remove SST stack orders-prod (tf owns it now)"; then
  run "sst remove" npx sst remove --stage prod
fi
```

```sh
swim run 2                                # asks: swim 2 requires human approval of task: ... Y/N [N]
ORDERS_ALLOW_SST_REMOVE=1 swim run 2      # approved up front (scripts, CI)
swim all -y                               # approve every question this run, logged as -y
```

### 5. Queue work and let a worker fire it

Get a batch ready while other work runs, then let it start as soon as swim
is idle:

```sh
swim queue 4 5 --approve ORDERS_ALLOW_SST_REMOVE --note "cutover batch"
swim watch                                # the worker: runs queued batches when idle
swim queue 6 --wait --timeout 30m         # queue, then block until it finishes (exit = its result)
swim wait                                 # block until nothing runs and nothing is queued
swim log 3f2a9c1e --until-done            # wait for one job; exits with its round's status
```

### 6. Approve from anywhere, as a team

Approvers sign the exact script, from their own machine, with their SSH key:

```sh
swim approve 4 --guards FIN_ALLOW_DROP    # approve lane 4's exact script; co-approvers run the same
swim approve --auto bob                   # standing approval: every job bob queues, until revoked
swim revoke --auto bob
swim watch --approved                     # run scripts as soon as they're fully approved
swim verify                               # prove the record hasn't been tampered with
swim history --oneline                    # who approved and ran what, like git log
```

`lookup.yml` says whose key is whose and how many approvals a guard needs
(see [Approvals](#approvals)).

### 7. Run rounds in CI

```sh
swim ci --changed --junit swim.xml        # only the rounds this push changed
```

Each lane's log gets its own foldable section, and failures become
annotations. See [CI](#ci).

### 8. Watch many workers from one place

```sh
swim hub --addr 0.0.0.0:7700 --token "$SWIM_HUB_TOKEN"          # the dashboard
swim hub enroll http://hub:7700 --token "$SWIM_HUB_TOKEN"         # in each repo with a worker
swim watch                                                        # now reports to the hub
```

### 9. Drive swim from scripts and agents

Every read command has a versioned YAML form, and long waits have proper
exit codes:

```sh
swim run 1 2 --yaml           # a stream of events: start, step, finish, summary
swim status --yaml
swim log 2 --yaml             # the round parsed: steps, commands, output, results
swim plan --yaml
```

---

## Features

### Lanes, rounds and jobs

| Term | Meaning |
|---|---|
| **lane** | A numbered slot, `swim 1..N`, with one script (`lane.N.sh`, or `lane.N.py`, `lane.N.ts`, a binary `lane.N`, ...) and one log. One worker owns it at a time. |
| **round** | What a lane script holds: one goal ("cut over service X"). Disposable. |
| **job** | A round's unique id (its `# Job:` line). It's recorded everywhere, exported to steps as `$SWIM_JOB`, and usable wherever a lane number is: `swim run 3f2a9c1e`. Naming a job pins the run to that exact round, so a lane rewritten in the meantime is refused. |
| **step** | One command run through `run`, `gate` or `snapshot`, logged with its command, output and exit code. |
| **gate** | A step whose failure stops the round before any later (destructive) step. |
| **guard** | A named approval in front of a destructive step (below). |

### Lane scripts

`swim new` writes a template. A filled-in round:

```bash
#!/usr/bin/env bash
# swim: syntax 2
# Round:   Cut over orders-api from SST to Terraform
# Job:     3f2a9c1e-7b4d-4e2a-9c1e-7b4d4e2a9c1e
# After:   1
# Owner:   zach
# Guards:  ORDERS_ALLOW_SST_REMOVE  remove SST stack (tf owns it)
# Locks:   tf/orders
# Timeout: 30m
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2

stage snapshot
snapshot "cfn template" aws cloudformation get-template --stack-name orders-prod
stage check
run  "tf plan" terraform -chdir=infra/orders plan -detailed-exitcode
gate "DeletionPolicy is Retain" bash -c 'aws cloudformation get-template --stack-name orders-prod | grep -q Retain'
stage change
if guard ORDERS_ALLOW_SST_REMOVE "remove SST stack orders-prod (tf owns it)"; then
  run "sst remove" npx sst remove --stage prod
fi
stage verify
run "re-plan is a no-op" terraform -chdir=infra/orders plan -detailed-exitcode
summary
```

- **Header:** only `Round` is required. Missing keys get defaults: a `Job`
  is generated when the round starts, and guards are found from `guard`
  calls in the body. Unknown keys are kept and shown in the log.
- **Stages:** every round uses the same four, in order: `snapshot`
  (read-only capture), `check` (checks and gates), `change` (guarded
  changes) and `verify` (prove it worked). Skip the ones you don't need.
  Running them out of order logs a `WARN`.
- **Time limits and retries:** `run`, `gate` and `snapshot` take
  `--timeout 2m`, `--retry 3`, `--backoff 5s` and `--retry-on 1,255` before
  the label. `# Step-Timeout: 5m` sets a default for every step, and the
  round's `Timeout` always wins. Only retry idempotent steps.
- **Library:** the functions are `lane_init`, `stage`, `run`, `gate`,
  `snapshot`, `guard`, `confirm`, `last_failed`, `any_failed`, `drift`,
  `stop` and `summary`. It runs on macOS's bash 3.2.
- **Failures:** a failed `run` doesn't stop the round; a failed `gate` does.
  Don't use `set -e`.
- **Native SDKs:** Python (`import swim`), TypeScript and Go libraries give
  lane programs swim's steps as calls, plus in-process steps for native
  checks (a `boto3` call logged as a step). They talk to the lane's socket
  (`swim.lane/v1`), and swim still decides everything. `# Includes:` puts
  local modules in the script's hash. See the [Python](docs/examples/python.md),
  [Node](docs/examples/node.md) and [Go](docs/examples/go.md) examples.
- **Approve the plan, not just the script:** `evidence "tf plan" terraform
  plan ...` in the check stage records what the change will do.
  `swim approve` shows it and signs its hash with the script, and the
  approval counts only while the run's plan still matches. If it drifted,
  the log shows a diff and the guard asks again.
- **Matrix rounds:** `# Matrix: SVC=orders,billing,users` runs one script
  once per value. Each cell has its own log, status and result.
  `# Matrix-Parallel: 2` runs cells two at a time, `swim run 3 --failed`
  retries only the failed cells, and `After: 3[orders]` waits for one cell.
- **Pre-approve in the live view:** select a lane that hasn't asked yet
  and press `y` (or `n`) to answer its guards ahead of time, then walk away.
  The answer is pinned to the script and lasts this run only.
- **Read-only preview:** `swim plan --check` runs every round's `snapshot`
  and `check` stages for real, stops where `change` would begin, and shows
  which gates pass today (`check PASS`, `stops at gate "..."`). Guards never
  approve, and lane status is untouched. `swim ci --check` does the same
  for a pull request.
- **Outputs between lanes:** `# Outputs: stats.json` promises files in
  `$SWIM_OUT`, and a dependent lane reads them with
  `"$(input 2 stats.json)"` (or `output KEY value` / `$(input 2 KEY)` for
  small values). Outputs are hashed into the chain on both sides, so
  `swim verify` proves the report was built from exactly those stats.
- **Status messages:** `msg --progress 7/25 "migrating billing"` (or
  `swim msg` from any language, even inside a step) shows where a long job
  is now, in the live view, `swim status` and the hub. The latest wins, and
  the log keeps throttled `MSG` lines.
- **Outcome handlers** (`# swim: syntax 3`): `on_success`, `on_failure` and
  `on_exit` functions run however the round ends, like try / catch /
  finally. They run soft, so every rollback step gets its attempt, and
  `reached change || return 0` skips a rollback when nothing changed. A
  failed round whose `on_failure` failed exits 89. `# Fail: hard` ends the
  round at the first failed step (`run --soft` opts one out), and
  `# Handler-Timeout:` (default 5m) is the handlers' budget. Lanes in other
  languages name commands: `# On-Failure: python3 rollback.py`.
- **Checks:** `swim lint` checks scripts before you run them: header,
  stages, guards, bash 3.2, blocked commands, and secrets echoed to the log.

### Lanes in other languages

A lane script can be any runnable: `lane.N.py`, `lane.N.js`, `lane.N.ts`, a
compiled `lane.N`, a plain bash script. `swim new 3 "<goal>" --lang py`
(or `js`, `ts`) writes a template.

Worked examples, each with the native SDK and with the plain CLI:
[Python](docs/examples/python.md), [Node (TypeScript and
JavaScript)](docs/examples/node.md) and [Go](docs/examples/go.md).

How swim runs it:

1. `lane.N.sh` runs with bash, as always.
2. A `#!` line wins, and swim asks nothing.
3. Otherwise, `runners:` for the extension: your own (saved per repo in
   your config) wins over the repo's committed `swim.yml`.
4. A compiled or executable file runs as itself.
5. The first time swim meets an extension with none of these, it looks up
   the usual runners on `PATH` and asks:

   ```
   swim: lane.2.ts is the first .ts lane here: it has no #! line, and no runner is set for .ts files.
     1) /usr/local/bin/tsx
     2) /opt/homebrew/bin/bun
   Run .ts lanes with /usr/local/bin/tsx? [Y/n, a number, or type a command]:
   ```

   It saves the answer as `runners.ts` for this repo. In CI, or under
   `swim watch`, nobody is there to ask, so the lane fails at start with the
   exact fix: a `#!` line, `swim config runners.ts tsx`, or
   `runners: {ts: tsx}` in `swim.yml`.

swim runs the program inside a wrapper. The wrapper opens and closes the
round, exports `SWIM_LANE`, `SWIM_JOB` and the rest, and keeps the git shim
in front. Steps are swim commands, so any language can call them:

```python
#!/usr/bin/env python3
# Round:  Rotate the orders bucket
# Guards: ORDERS_ALLOW_DELETE  delete the old bucket (2026-10-13: replaced)
import os, subprocess
def swim(*a): return subprocess.run([os.environ.get("SWIM_BIN", "swim"), *a]).returncode

swim("stage", "check")
swim("gate", "new bucket exists", "--", "aws", "s3api", "head-bucket", "--bucket", "orders-v2")
swim("stage", "change")
if swim("guard", "ORDERS_ALLOW_DELETE", "delete bucket orders (2026-10-13: replaced)") == 0:
    swim("run", "delete old bucket", "--", "aws", "s3", "rb", "s3://orders")
```

- **The commands:** `swim run|gate|snapshot "<label>" -- cmd`,
  `swim guard FLAG "<action>"` (exit 0 when approved), `swim stage NAME`,
  `swim drift "<what>"` and `swim stop "<why>"`.
- **Ending the round:** a failed gate, `swim stop`, a blocked `git push` or
  your Ctrl-C ends the program, even if it ignores the exit code.
- **No swim calls:** a program that never calls `swim run` is logged as one
  step, with its whole output (secrets masked) and a result from its exit
  code.
- **Header:** `#` or `//` comments. A binary, or any lane, can keep its
  header in a sidecar `lane.N.yml` instead, and the sidecar's keys win:
  `round`, `job`, `after`, `guards` (a map of flag to action), `locks`,
  `timeout`, `step-timeout`.

### Logs

Logs are plain text, readable with `cat`. Each line's start says what it
is, and command output is always fenced with `| `, so
`grep '^  FAIL' .swim/logs/agent2.log` finds every failure:

```
== ROUND 2026-10-09T12:00:00Z  job=3f2a9c1e-...  Cut over orders-api
   script: lane.2.sh | owner: zach | after: 1 | timeout: 30m | git: main@abc1234 | sha256: e91a058b9c06

-- stage check  12:00:02
  FAIL  tf plan (exit 2)                        3.1s  12:00:02
        $ terraform -chdir=infra/orders plan -detailed-exitcode
        | Error: ...
  STOP  gate failed: tf plan

== END FAIL  pass=1 fail=1 skip=0 drift=0  exit=1  3.6s  2026-10-09T12:00:04Z
   stages: snapshot PASS | check FAIL | change none | verify none
```

- **`swim log N`** renders a log on a terminal: colours, times relative to
  the round's start, and long output folded (`--full` expands it). Piped,
  or with `--raw`, it prints the file as is.
- **The step running now** isn't in the file until it ends. `swim log` adds
  it, with its output so far, as `RUNNING  <label> (in progress)`.
- **`swim log N -f`** follows a lane live, like `tail -f`.
- **`swim log N --until-done`** waits for the round to end, then exits with
  its status.
- **`.swim.log`** is the project log: one line per top-level action. A new
  agent reads it first, and `swim note "<text>"` adds decisions to it.

### The live view

While lanes run, a panel pinned to the top of the terminal shows each
lane's state and round; output scrolls underneath, prefixed `[N]`.

- **Keys** (more than one lane on a terminal):
  - `→`/`←` open one lane's log, and `Esc` goes back to all lanes.
  - `↑`/`↓`, `PgUp`/`PgDn` and `Home`/`End` scroll.
  - Type a number and press `Enter` to jump to that lane. `f` filters to
    running or failed lanes, and `?` shows help.
- **Approvals:** a banner names a lane that's waiting for you. `Enter`
  opens it with the question underneath, and `y` or `n` answers it there.
- **No terminal, or `--plain`:** output is plain prefixed lines.
- **`--yaml`:** output is a stream of YAML events instead (see
  [Scripting](#scripting-and-yaml)).
- **Chime:** `swim config chime true` rings the bell when a run ends; set
  it to `failure` to ring only when something went wrong.

### Dependencies, parallelism and locks

- **`# After: 2 3`** waits for those lanes to pass in this run, and skips
  the round if one fails. A job id waits for that exact round. Ordering that
  holds for every round can go in config `deps`. A cycle stops the run
  before anything starts.
- **`--parallel N`** (or config `max_parallel`) caps how many lanes run at
  once. Lanes ready to start queue for a slot, longest chain first.
- **`# Locks: orders-table`:** lanes that share a lock never run at the same
  time, in either order. That holds within a run and across concurrent runs
  on the machine. A failure doesn't spread through a lock.
- **`# Always: yes`** makes a lane standing setup, like a CI
  `before_script` (lint, fmt, init). It never counts as passed: `swim all`
  runs it every time, first, and every other lane in the run starts only
  after it finishes. Lanes that name it in `# After:` need it to pass; the
  others start whatever its result. `swim run N` pulls in the Always lanes
  that N names, and no others. An Always lane waits only on other Always
  lanes, and `swim stub` and `swim archive` refuse it without `--force`.
- **`# Rerun-Children: yes`** (Always lanes only): `swim all` also reruns
  every lane downstream of this one, at any depth, even if it passed. That's
  a test suite written as rounds: lint first, then the tests again. `swim
  lint` warns about a guarded round under one (`rerun-guard`).

```
$ swim plan
[+] swim 1  Diamond root: fetch pages
├── [+] swim 2  Child A: convert to Markdown
│   └── [+] swim 4  Join: build report  wait [2,3]
└── [~] swim 3  Child B: link stats
    └── [+] swim 4 (shown above)

Plan: 3 to run, 1 to retry, 0 to rerun, 0 to skip.
```

With Always lanes, `swim plan` draws them first (`[*]`), says which passed
lanes they rerun, then the rest:

```
[*] swim 1  lint
└── [+/-] swim 2  unit  rerun (swim 1)
[*] swim 5  init
then, once swim 1, 5 finish (whatever the result):
[+] swim 4  report
```

`swim timeline` draws when each lane waited (on dependencies, a slot or a
lock), ran and finished. It marks the longest chain, and `--html` writes a
Gantt chart you can share.

### Env files

Lanes get non-secret context (`STAGE`, `AWS_PROFILE`, table names) from
`KEY=VALUE` files. swim parses them and never runs them as shell:

```ini
# ~/.config/swim/env.swim: general keys, then a section per repo
STAGE=dev
[repo ~/dev/orders]
AWS_PROFILE=orders-prod

# <repo>/.env.swim (git-ignored): a section per lane
[lane 2]
TABLE=orders-v2
```

- **Precedence:** the shell wins, then the lane's section, the repo's
  section, `env:` in the committed `swim.yml`, and the general keys.
- **No approvals from files:** a key that is a lane's guard flag (or matches
  a `lookup.yml` guard glob) stops the run before anything starts.
- **Masked and recorded:** file values are masked in logs. The round header
  lists the files that applied, and status and the chain record a hash of
  the env. A queued lane whose env changed since it was queued is skipped.
- **`swim env N`** shows what a lane gets and where each key comes from.
- **`.lane.N.rc` is deprecated.** A guard flag it sets no longer approves,
  and `swim doctor --fix` moves a plain one into `.env.swim`.

### Secrets

Secrets live encrypted to your SSH key, not in env files or the shell:

```sh
swim secret set db_password            # this repo (hidden prompt, or stdin)
swim secret set datadog_api_key --all  # every repo
swim secret ls                         # names and scopes, never values
```

A lane gets them by name: from bash with the CLI, and from other
languages with the SDK:

```bash
DB=$(swim secret get db_password) || stop "no db password"   # check it: lanes don't run with set -e
```

```python
password = swim.secret.get("db_password")          # TypeScript: await secret.get(...)
                                                   # Go: swim.Secret.Get(...)
```

- **Encrypted with [age](https://age-encryption.org)** to an `ssh-ed25519`
  or `ssh-rsa` key (`secrets_key`, else `signing_key`, else
  `~/.ssh/id_ed25519`). They're kept in `secrets.yml` next to your config,
  mode 0600. Names stay readable; each value is encrypted on its own.
  Decrypting needs the private key on disk, because ssh-agent can't
  decrypt.
- **Two scopes:** `--all` and `--repo` (this git repo, the default inside
  one). A repo's own secret wins over an `--all` one with the same name.
- **Decrypted once per run:** `swim run` decrypts at start and serves the
  lanes over a private socket. It asks a protected key's passphrase before
  the live view, and only if a lane looks like it uses secrets. Outside a
  run, `swim secret get` and the SDKs decrypt with your key directly.
- **Masked exactly, by name:** every lane process masks all of the repo's
  secrets as `***(db_password)`, including their base64 and URL forms,
  whichever ones it used.
- **Audited:** `.swim.log` records which lane got which secret and how,
  never the value. `swim lint` warns about names the store doesn't have.
- **CI:** `SWIM_SECRET_<NAME>` stands in for a secret, with no key needed.
- **The limits:**
  - Secrets keep values out of logs, the hub and git. They don't protect
    against lane code, which runs as you and could read your key itself.
  - A value passed as a command argument (`psql "$DB"`) is visible in `ps`
    while it runs; prefer the tool's env variable or a file when it has one.
  - `swim watch` and CI can't ask a passphrase.

### Guards and approvals

<a id="approvals"></a>

A `guard FLAG "<action>"` approves its step in one of these ways, in this
order:

1. **The flag is set.** `FLAG=1` approves up front, for CI and scripts.
   `FLAG=0` declines without asking.
2. **A signed approval.** Approvals of this exact script, plus standing
   approvals for whoever queued the batch, are counted against
   `lookup.yml`'s rule for the guard.
3. **The person at the terminal** answers
   `swim 2 requires human approval of task: <action>  Y/N [N]`. Questions
   from parallel lanes take turns, or wait on their lane in the interactive
   view.
4. **`-y`:** `swim all -y` (or `swim watch -y`) approves every question,
   and logs that `-y` did.

Anything else is a **dry run**: the step is skipped, and the log says how to
approve it. A queued batch never asks a question.

Signed approvals are files under `approvals/`. Commit them, so approvers can
add theirs from anywhere. Each file is an ordered, hash-linked log of SSH-signed
approve and revoke events, so its order is part of what it proves, like git
commits:

```sh
swim approve 4 --guards FIN_ALLOW_DROP     # this exact script (by SHA-256); co-approvers run the same
swim approve --auto bob [--guards A,B]     # every job bob queues, until revoked
swim revoke 4 / swim revoke --auto bob     # withdraw yours
swim approve                               # list approvals and who stands behind each
```

`lookup.yml` (committed at the repo root) says whose key is whose, groups
people, and sets what each guard needs:

```yaml
users:
  zach:  {keys: ["ssh-ed25519 AAAA... zach@laptop", "sk-ssh-ed25519@openssh.com AAAA... zach@yubikey"]}
  alice: {keys: ["ssh-ed25519 AAAA... alice@work"]}
  bob:   {keys: ["ssh-ed25519 AAAA... bob@ci"]}
groups:
  platform-admin: [zach, alice]
require:
  - guards: [FIN_ALLOW_*]
    group: platform-admin
    count: 2              # two different people from platform-admin
```

Script approvals and standing approvals count together: zach's standing
approval of bob's jobs plus alice's approval of the script make two
platform-admins. If a script is edited after it was approved, the approval
no longer matches it. `swim keys` shows who's who.

### Chain of custody

Every approval and every round is recorded in `.swim/chain.jsonl`, one entry
per line. Each entry is signed with the runner's SSH key and holds the
SHA-256 of the entry before it:

- **round:** the script's hash, the hash of the round's log section, the
  result, and the approvals it used. A copy of each script that ran is kept
  by hash under `.swim/scripts/`.
- **approve:** who approved which guard of which script, and how: a
  signature, standing approval, prompt, flag, `-y`, or a queued batch.

```sh
swim verify                 # every link, signature, log section and kept script checks out (exit 1 if not)
swim history                # the chain, newest first, like git log (--oneline, -n N, --reverse)
```

Editing a log, removing an entry or reordering entries is caught and
reported. Signing uses `ssh-keygen -Y`, the same format git uses for SSH
commit signing, so anyone can check a signature without swim. Config:
`signing_key` (default `~/.ssh/id_ed25519`; a `.pub` path signs through
ssh-agent) and `signing: auto | required | off`.

### Queue, watch and wait

```sh
swim queue add 4 5 --approve FLAG [-y] [--note "..."]   # or: swim queue 4 5 ...
swim queue                                               # waiting, running, orphaned, done
swim queue show|rm|requeue ID
swim watch [--once] [-y] [--approved] [--no-tui]         # the worker
swim wait [--timeout D]                                  # block until idle and nothing queued
```

- **A batch is fixed when you queue it:** each lane is pinned to its job and
  its exact script. A lane edited afterwards is skipped, not run.
  Approvals travel with the batch, and the worker's own environment
  approves nothing.
- **The batch is signed by whoever queued it,** so standing approvals for
  that person apply to it.
- **`swim watch`** waits until swim is idle, runs the oldest batch, and
  then consumes it into `.swim/queue/done/` with its result.
  - **Several workers** can share one queue. Claiming an entry is atomic,
    and one batch runs at a time per repo.
  - **Ctrl-C** stops the current batch gracefully.
  - **`--approved`** also queues scripts as soon as they're fully approved.
  - **On a terminal,** the watch lists every lane's job and the queue while
    it waits. Use ↑ ↓ to pick one, `r` to run it now (a job as `swim run N`,
    a waiting batch out of turn) and `q` to queue a job. A batch's live view
    lets ← → pick a lane to read, but never answers its guards.

### Interrupts

- **First Ctrl-C:** every lane stops at its next step boundary. Running
  steps finish, nothing new starts, and lanes waiting to start are skipped.
- **Second:** interrupts the running steps, and kills them after
  `interrupt_grace` (5s).
- **Third:** kills them at once.
- **Other signals:** SIGTERM (a CI cancel) stops gracefully, then forces
  after `term_grace`. `swim interrupt N` stops one lane from another
  terminal.

### Safety

- **swim never pushes, commits or pulls.** `git push`, `git commit` and
  `git pull` are caught three ways, and the round stops:
  - in a script, before the lane starts;
  - in a command built at runtime, at the step (`BLOCKED`, exit 87);
  - by a git shim on each lane's `PATH`.
  
  A repo can block more commands with `blocked_commands`.
- **Secrets are masked** as `***` everywhere swim writes. That covers the
  values of `secret_env` variables and of names like `*_TOKEN`, plus
  credential shapes such as AWS key ids and GitHub tokens. Secrets from
  [`swim secret`](#secrets) are masked exactly, as `***(name)`. It's a
  backstop: still never print secrets.
- **Fail-closed:** gates and `confirm` stop the round when there's no
  answer.
- **Running lanes are protected:** swim won't edit, archive, stub or rerun a
  lane that's running.
- **Environment check:** `swim doctor` checks the setup: binary, `PATH`,
  config, `.gitignore`, stale pid files, toolchain and lock.

### CI

<a id="ci"></a>

`swim ci` is `swim all` for a CI job:

- It never prompts.
- Each lane's log goes in a foldable section.
- FAIL and DRIFT become annotations.
- It writes a job summary, plus an optional JUnit report.

It detects GitHub Actions and GitLab CI, and falls back to generic output.
Commit a `swim.yml` with the repo's settings (the same keys as a config
`repos:` entry), and the lane scripts the job runs.

```yaml
# .github/workflows/swim.yml
jobs:
  swim:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }          # --changed needs the history
      - run: go install github.com/gates-brightly/swimlane/cmd/swim@latest
      - run: swim ci --changed --junit swim.xml
        env:
          FIN_ALLOW_DELETE: ${{ secrets.FIN_ALLOW_DELETE }}   # guard flags come from the job
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: swim
          include-hidden-files: true
          path: ".swim/logs\n.swim.log\nswim.xml"
```

`--changed` runs only the rounds whose script the push or merge request
changed, plus any unpassed lanes they wait on (Always lanes count as
unpassed). `--rerun` runs passed rounds again; for a test suite written as
rounds, an Always lane with `Rerun-Children: yes` at its root does that
without the flag. A fresh CI checkout has no passed rounds, so everything
runs there either way. swim never commits results back.

This repo dogfoods it. Its own CI is `swim ci` over committed rounds. Three
Always lanes run first, in order: `lane.1.sh` (fmt), `lane.2.sh` (lint) and
`lane.3.sh` (unit tests). Then `lane.4.sh` (e2e) runs. `make ci` runs the
same rounds locally.

### The watch hub

```sh
swim hub [--addr 127.0.0.1:7700] [--token T]          # serve the dashboard (and a live screen on a terminal)
swim hub enroll URL --token T [--name billing]       # in a repo; then swim watch reports to it
swim hub status | leave
swim hub login URL --admin-token T                   # the operator, once
swim hub approve billing                             # or press a on its row in the hub's screen
swim secret grant billing db_password                # encrypted here to billing's key; the hub keeps ciphertext
```

- **Dashboard:** a card per worker, showing its activity, lanes and queue.
  Workers that stop reporting show as stale.
- **Live screen:** on a terminal, `swim hub` lists every worker with the
  selected one's running lanes and queue. Enter opens a worker to show each
  lane's message and log tail, and Esc comes back. `--no-tui` turns it off.
- **Worker pages:** each lane's step, the live tail of running lanes, the
  queue, and recent events.
- **`GET /api/workers`:** the same data as JSON.
- **Push, not pull:** workers report to the hub, so they can sit behind NAT.
  A restarted hub fills back up as workers report.
- **Access:** workers need the token. The pages are read-only, and bind to
  localhost by default.
- **Secrets for workers:**
  - **Keys:** enrolling gives each worker its own age key (with no
    passphrase, so `swim watch` can decrypt unattended) and a token of its
    own. A worker stays *pending* until you approve its key fingerprint on
    the hub.
  - **Grants:** `swim secret grant` encrypts a secret on your machine to
    that worker's key. The hub keeps only ciphertext, in `--state DIR`, and
    never holds a key that could decrypt it.
  - **At run time:** the worker's runs fetch and decrypt their grants at
    start. Lanes get them the usual way (`swim secret get`, `secret.get`),
    masked by name.
  - **Changes:** `swim secret revoke` removes a grant. A worker that comes
    back with a new key drops to pending and loses its grants. The hub's
    `audit.log` records approvals, grants and fetches.

### Scripting and YAML

<a id="scripting-and-yaml"></a>

| Command | YAML schema |
|---|---|
| `swim run\|all --yaml` | `swim.run/v1`: one document per event (`run`, `waiting`, `queued`, `locked`, `start`, `step`, `finish`), then `summary`. `--yaml-output` adds lane output. |
| `swim status --yaml` | `swim.status/v1` |
| `swim log N --yaml` | `swim.log/v1`: the round parsed, including the running step as `RUNNING` |
| `swim plan --yaml` | `swim.plan/v1` |
| `swim timeline --yaml` | `swim.timeline/v1` |
| `swim history --yaml` | `swim.history/v1` |
| `swim queue --yaml` | `swim.queue-list/v1` |
| `swim lint\|doctor --yaml` | findings |

Schemas only grow within a version; removing or renaming a field bumps it.
Exit codes are dependable: `0` means everything selected passed. Waits
(`swim wait`, `queue --wait`, `log --until-done`) exit with the result
they waited for.

---

## Reference

### Commands

| Command | What it does |
|---|---|
| `swim init` | Set up the repo: config entry, `.gitignore` block, `.swim/status.yml`. |
| `swim new N "<goal>" [--lang py\|js\|ts] [--job ID]` | Write `lane.N.sh` (or `.py`, `.js`, `.ts`) from the template with a new job id. |
| `swim plan [N\|JOB ...] [--rerun] [--yaml]` | Dry run: what would run, retry, rerun or be skipped, as a dependency tree. |
| `swim all [--rerun] [--parallel N] [-y\|--no-prompt] [--no-tui] [--yaml]` | Run every pending round that hasn't passed. |
| `swim run N\|JOB ...` / `swim N ...` | Run specific lanes, even if they passed. A job id pins the exact round. |
| `swim status [N\|JOB] [--yaml]` | Last state of every lane. |
| `swim log [N\|JOB] [--all] [--full] [--raw] [--yaml] [-f] [--until-done]` | A lane's or job's log (with the running step), or the project log. |
| `swim timeline [RUN\|N\|JOB] [--steps] [--yaml] [--html FILE]` | When each lane waited, ran and finished; the longest chain. |
| `swim archive N\|JOB ... [--as NAME] [--force]` / `swim stub N "<msg>" [--force]` | Archive lanes' logs (all or nothing); replace a script with a "nothing pending" stub. An Always lane needs `--force`. |
| `swim note [--lane N] "<text>"` | Record a decision in the project log. |
| `swim queue [add\|show\|rm\|requeue]`, `swim queue N ... [--approve F] [-y] [--wait] [--timeout D]` | Queue batches to run when swim is idle. |
| `swim watch [--once] [-y] [--approved] [--no-tui]` | The queue's worker; on a terminal, pick a job to run (`r`) or queue (`q`). |
| `swim wait [--timeout D]` | Block until nothing runs and nothing is queued. |
| `swim approve [N\|JOB --guards A,B \| --auto USER]` / `swim revoke ...` | Signed approvals: this exact script, or standing for a user's queued jobs. |
| `swim verify [--strict]` / `swim history [--oneline]` / `swim keys` | The chain of custody: prove it, read it, and see who's who. |
| `swim hub [enroll\|leave\|status]` | The watch hub. |
| `swim ci [N\|JOB ...] [--changed[=BASE]] [--rerun] [--junit FILE]` | Run rounds in a CI job. |
| `swim interrupt N\|JOB` | Stop one lane at its next step boundary. |
| `swim lint [--strict]` / `swim doctor [--fix]` | Check lane scripts / the environment. |
| `swim config [KEY [VALUE]] [--repo]` | Show or set config, keeping the file's comments. |
| `swim env [N\|JOB] [--yaml]` | The env a lane gets from env files, masked, with each key's source. |
| `swim secret set\|get\|ls\|rm NAME [--all\|--repo]` | Secrets encrypted to your SSH key, for lanes. |
| `swim lock [--upgrade]` / `swim changelog` | The repo's breaking-version lock; what changed between versions. |
| `swim step -- cmd ...` / `swim lib` | Run and log one command; print the lane library. |
| `swim run\|gate\|snapshot "<label>" -- cmd`, `swim guard\|stage\|drift\|stop ...` | In-lane steps for lane scripts in any language. |
| `swim --help` / `swim <cmd> --help` | The full guide, or one command's detail. |

### Files

| Path | Contents | Commit? |
|---|---|---|
| `lane.N.sh` (or `lane.N.<ext>`, `lane.N`) | Lane N's current round. | Usually not; yes for CI rounds. |
| `lane.N.yml` | Optional metadata for lane N's script (a binary's header). | With its script. |
| `swim.yml` | The repo's shared settings. | Yes |
| `lookup.yml` | Users, keys, groups and approval rules. | Yes |
| `approvals/` | Signed approvals. | Yes |
| `.swim.lock` | The breaking version of swim the repo uses. | Yes |
| `.swim.log` | The project log: one line per top-level action. | No |
| `.env.swim` | Lane-scoped env (`[lane N]` sections). | No |
| `~/.config/swim/secrets.yml` | Your secrets, encrypted (`swim secret`). | Never |
| `.swim/status.yml` | Last state of every lane (`cat` it). | No |
| `.swim/logs/` | Lane logs, current and archived. | No |
| `.swim/chain.jsonl`, `.swim/scripts/` | The chain of custody, and every script that ran. | No (export it to keep it) |
| `.swim/queue/`, `.swim/runs/`, `.swim/snapshots/` | Queued batches, run records, snapshot output. | No |
| `.swim/hub.yml` | This directory's hub enrollment. | No |

### Config

Personal settings live outside the repo, so workers can't change them:

```yaml
# ~/.config/swim/config.yml  ($XDG_CONFIG_HOME/swim/config.yml)
defaults:
  lanes: 4
  header_env: [STAGE, AWS_PROFILE]       # env recorded in every step header (never secrets)
  chime: failure                         # true | false | failure
  approvals: prompt                      # prompt | flags (never ask)
  interrupt: graceful                    # graceful | immediate
  signing_key: ~/.ssh/id_ed25519.pub     # signs the chain and approvals (a .pub: via ssh-agent)
  secrets_key: ~/.ssh/id_ed25519         # swim secret encrypts to it (the private key must be on disk)
repos:
  /path/to/your-repo:                    # overrides defaults and the repo's swim.yml
    max_parallel: 8
    secret_env: [DATADOG_API_KEY]
    deps: {2: [1], 4: [2, 3]}
    runners: {ts: /usr/local/bin/tsx}    # saved when swim asks; swim.yml's runners: are the default
```

`swim config KEY VALUE` sets a value and keeps the file's comments. A
committed `swim.yml` takes the same keys as a `repos:` entry, and it's how a
CI runner with no config gets its settings.

### Install and versions

<a id="install-and-versions"></a>

```sh
go install github.com/gates-brightly/swimlane/cmd/swim@latest     # or @v0.7.20261013 to pin
git clone git@github.com:gates-brightly/swimlane.git && cd swimlane && make install   # from a checkout
make link / unlink    # symlink this checkout's bin/swim into ~/.local/bin
make which            # every swim on your PATH, and which one runs
swim changelog        # what's new in the version you're running (--since <old version>)
```

- **Version numbers:** `swim --version` prints `<breaking>.<YYYYMMDD>`.
  The breaking number changes when lane scripts, logs or swim's state
  change incompatibly, and for a second release on the same day.
- **`.swim.lock`:** the first run in a repo writes it, recording the
  breaking version the repo uses. Commit it. A swim with a different
  breaking number refuses to run lanes, and says whether to update swim or
  run `swim lock --upgrade`.
- **Syntax versions:** lane scripts and logs declare their syntax
  (`# swim: syntax 2`). Older files are migrated automatically, with the
  originals kept under `.swim/migrations/`.

### Development

```sh
make test             # unit tests (fast)
make e2e              # the Go e2e suite and every scenario (S="dag99 ..." for some)
make check            # gofmt, vet, unit and e2e tests
make ci               # this repo's CI rounds, through swim ci
make release V=v0.<breaking>.<YYYYMMDD>   # tag a release (checks version, CHANGELOG, clean tree; never pushes)
```

- **Two layers of end-to-end tests:**
  - `internal/e2e` (Go) checks commands one at a time.
  - `e2e/scenarios` runs whole lane DAGs offline: 99 lanes, injected
    failures, retries, interrupts, locks and CI. See
    [`e2e/README.md`](e2e/README.md).
- **Bash 3.2:** `internal/assets/lib.sh`, `e2e/run.sh` and
  `e2e/lib/common.sh` must stay compatible with macOS's bash 3.2.
- **Breaking changes:** bump `Breaking` in `internal/version/version.go`,
  add the change to `swim lock --help` and `CHANGELOG.md`, and follow the
  release steps at the top of the changelog.
