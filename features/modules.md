# Modules: reusable Go and bash utilities for lanes, pinned like the script

Status: in progress. Phases one and two are built: `# Modules:`, `mod` and
`mod_env`, the built-in `aws-session`, `clone` and `notify`, repo modules
(bash and Go), the manifest pins, `swim.yml` `modules:`, `swim modules
ls|show|new`, plan, approve, lint, doctor, and op `module` in the SDKs.
More built-ins since: `target`, `backup`, `wait`, `tf`, `smoke` and
`claude` (which covers `writeup`'s job; see "As built"). Left: phase three
(fetched modules, `swim modules get`, the lock), and `writeup` reading
observe.md's observations once they exist.

## Summary

A **module** is a small, versioned utility any lane can use: start an AWS
session, clone a repo at a commit, post a notification, write up a run's
observations with Claude. A lane declares the modules it uses, and swim
gives it exactly those, pinned into the execution manifest, so an approval
covers the module's bytes as it covers the script's.

```bash
#!/usr/bin/env bash
# Round: deploy orders, then tell the channel
# Modules: aws-session clone@1 notify
# Guards: ORDERS_ALLOW_DEPLOY  deploy orders-prod
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 3

stage check
mod_env aws-session --profile orders-prod --role arn:aws:iam::123456789012:role/deploy-read
mod clone https://github.com/org/orders 3f2a9c1e0b7d4e2a9c1e0b7d4e2a9c1e0b7d4e2a "$SWIM_OUT/orders"

stage change
if guard ORDERS_ALLOW_DEPLOY "deploy orders-prod"; then
  run "deploy" ./deploy.sh "$SWIM_OUT/orders"
fi
stage verify
mod notify "#orders" "orders deploy: $SWIM_OUTCOME (job $SWIM_JOB)"
summary
```

```
$ swim plan 3
 swim 3  run  deploy orders, then tell the channel
    │ modules  aws-session 1.0.0 (built in: credentials, network sts.amazonaws.com)
    │          clone 1.2.0 (built in: network, writes $SWIM_OUT)
    │          notify 1.0.0 (built in: network hooks.slack.com; secret SLACK_WEBHOOK_URL)
    │ guards   ORDERS_ALLOW_DEPLOY  deploy orders-prod
```

- **Go and bash only.** A module is a bash file of functions, or a Go program.
  Lanes in any language can call either: bash lanes through the library,
  Python, TypeScript and Go lanes through the lane protocol
  (`swim.lane/v1`, op `module`).
- **Three sources, in phases:** built into the swim binary (phase one), in
  the repo (`modules/<name>/`, phase two), and fetched before approval from
  a source address, like Terraform modules (phase three). A module is never
  fetched at run time.
- **Every call is a step.** `mod` and `mod_env` run the module through `swim
  step`, so it's logged, masked, timed and counted like any `run`.

## Motivation

- **Planners rewrite the same plumbing every round.** Assuming a role,
  cloning at a commit, posting to Slack, polling until something is ready:
  20 lines of shell, written slightly differently each time, re-read by
  every approver, and wrong in a new way now and then (a clone of a branch
  tip, a token echoed in a curl -v).
- **Approvers should read a module once.** A reviewed, versioned module with
  declared effects ("network: sts.amazonaws.com, credentials") is faster to
  approve than its inline equivalent, and the same everywhere it's used.
- **Integrity has to survive reuse.** The brief's *Integrity* guarantee is
  that what runs is what was approved. A helper sourced from somewhere the
  manifest doesn't see breaks that, and fetching a helper at run time breaks
  it completely (the reason running real GitHub Actions `uses:` was ruled
  out). Modules exist so reuse goes through the manifest, not around it.
- **Observations want a write-up step** ([observe.md](observe.md)). Turning
  a run's computed observations into a report with Claude needs an API
  call, masking, a guarded credential and an artifact. That's a module, not
  a script each repo writes.

## Behaviour

### What a module is

```
modules/notify/                 (or built in, or fetched: the same layout)
  module.yml                    what it is and what it does
  notify.sh                     a bash module: functions, sourced by the lane
      — or —
  main.go, go.mod               a Go module: one program, built by swim
```

```yaml
# module.yml
schema: swim.module/v1
name: notify
version: 1.0.0
kind: bash                       # bash | go
summary: post a message to a Slack or generic webhook
commands: [notify]               # what `mod NAME` may call (bash: functions)
effects:                         # declared, shown in plan and approvals
  network: [hooks.slack.com]     # hosts it talks to (egress)
  secrets: [SLACK_WEBHOOK_URL]   # names it gets from the secrets broker
  writes: []                     # paths outside $SWIM_OUT it may write
  credentials: false             # true: it produces credentials (mod_env)
  read_only: false               # true: safe before `stage change` (swim plan --check)
guards: []                       # guard flags it asks, if any
```

- **Effects are declared, not enforced.** swim shows them in `swim plan`,
  the approval prompt and `swim lint`. It can't sandbox a module, which
  runs as the lane, with the lane's permissions. What swim does enforce is
  that the module's bytes are the ones approved, and that its secrets come
  only through the broker, guarded as the repo says.
- **Bash modules** are bash 3.2, like the library. They're sourced into the
  lane once, by `lane_init`, from the staged copy. Their functions are
  namespaced (`notify`, `notify_thread`); a function that would shadow a
  library function or another module's is refused at load.
- **Go modules** are one program each. swim builds them (below) and runs
  them through `swim step`. They get the lane's environment, and
  `SWIM_MODULE_OUT`, a file they may write `KEY=VALUE` lines to (see
  `mod_env`).

### Using one

- **`# Modules: <name>[@<major>] ...`** in the header (any runtime;
  `modules:` in `lane.N.yml`). A name alone takes the newest major the repo
  has; `@1` pins a major. The exact version comes from the lock (phase
  three) or the binary (built in).
- **`mod NAME [args...]`** runs a module command as a step labelled
  `NAME args`, or `mod --label "<label>" NAME ...`. It takes `run`'s options
  (`--timeout`, `--retry`, `--soft`). `mod --gate` makes it a gate.
- **`mod_env NAME [args...]`** is `mod` for modules that produce
  environment (`credentials: true`, or any module that writes
  `SWIM_MODULE_OUT`): after the step passes, the lane exports what the
  module wrote. Every value is masked from then on, like a secret. A
  module without `credentials: true` that writes `AWS_*`, `*_TOKEN` or
  similar names is refused.
- **Undeclared means unavailable.** `mod notify` in a lane whose header
  doesn't list `notify` is refused at the step (`module notify isn't
  declared in lane.3.sh`), and `swim lint` errors before it runs. This is
  the same rule as undeclared guards.
- **SDK lanes:** `r.module("notify", "#orders", "done")` (Python, TS) or
  `r.Module(...)` (Go) sends op `module` with `name`, `args`, `opts`,
  `env: bool`. swim runs it as a step, as above. With `env: true` the
  result carries the variables, which the SDK sets in the program's own
  environment.

### Who may use what (policy)

- **`swim.yml` `modules:`** is repo policy, committed and part of the policy
  hash (so changing it stales approvals, as for `secrets:`):
  ```yaml
  modules:
    allow: [aws-session, clone, notify, writeup, ./modules/*, github.com/org/swim-modules//*]
  ```
  Without a `modules:` key, built-in modules are allowed and nothing else.
  A lane naming a module the policy doesn't allow doesn't start, and lint
  says which rule is missing.
- **Secrets a module declares** are only released through the broker, and
  `swim.yml` `secrets:` decides whether they're guarded. A module's
  `secrets:` list is a request, not a grant: the broker refuses a name the
  repo doesn't give the lane.

### Pinning

- **Built in:** the manifest records `name`, `version` and the hash of the
  module's embedded bytes. Upgrading swim to a release that changes a
  module stales approvals of the lanes that use it, which is correct: what
  they run changed. A release that changes no module stales nothing.
- **Repo:** the module's whole directory is pinned, file by file, like
  `# Includes:` (`from: module`), and staged with the lane. An edit to
  `modules/notify/notify.sh` stales approvals of every lane that uses it.
- **Fetched:** pinned by its tree hash from the lock (below).
- **Go builds** are pinned by source, not binary. The manifest covers the
  module's source tree, which is portable across machines; the binary swim
  builds from it is recorded per machine under `local:` (like the runner's
  binary today), and rebuilt when its source hash or the Go version
  changes. Built-in Go modules are part of the swim binary and need no
  toolchain.

### Phase one: built-in modules

| Module | Kind | Does | Effects |
|---|---|---|---|
| `aws-session` | go | `mod_env aws-session --profile P [--role ARN] [--duration 1h]`: credentials from the operator's own profile or SSO, optionally assuming a role, exported as `AWS_ACCESS_KEY_ID`/`SECRET`/`SESSION_TOKEN` (masked). The role ARN is an argument, so it's in the script and the approval. | credentials, network `sts.amazonaws.com` |
| `clone` | bash | `mod clone URL SHA DIR`: clones and checks out exactly SHA (a full 40-hex commit; a branch or tag is refused), verifies `HEAD` is SHA, and logs the URL and SHA. Runs git as the lane (swim itself only reads git). | network (the URL's host), writes DIR |
| `notify` | bash | `mod notify CHANNEL|URL TEXT`: posts to a Slack incoming webhook or a generic JSON webhook. The webhook URL is a secret (`SLACK_WEBHOOK_URL`), never an argument. Fails soft by default, since a missed notification shouldn't fail a deploy. | network, secret `SLACK_WEBHOOK_URL` |
| `writeup` | go | `mod writeup [--observations FILE] [--out FILE]`: turns a run's observations ([observe.md](observe.md)) into a Markdown report with Claude (below). | network `api.anthropic.com`, secret `ANTHROPIC_API_KEY`, writes `$SWIM_OUT` |

`swim modules` lists what's available, with versions and effects. `swim
modules show NAME` prints a module's `module.yml` and, for bash, its source,
so an approver can read it without leaving the terminal.

### The `writeup` module

What [observe.md](observe.md) calls "the write-up", delivered as a module so
no repo writes its own:

```bash
# Round: write up run observations
# Finally: 1 2 3
# Modules: writeup
# Guards: OBSERVE_ALLOW_WRITEUP  send this run's observations and masked excerpts to the Claude API
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 9
stage verify
if guard OBSERVE_ALLOW_WRITEUP "send run $SWIM_RUN's observations to the Claude API"; then
  mod writeup --out "$SWIM_OUT/writeup.md"
fi
summary
```

- **Input:** `SWIM_OBSERVATIONS` (the finally lane's upstream observations)
  and, for each observation, the excerpt swim selected (the cited step's
  command and its last output lines). Everything passes through swim's
  masker first. It never sees whole logs.
- **The call:** the Messages API with no tools, a fixed system prompt that
  asks for a report citing observation ids for every claim and labelling
  hypotheses, and a model pinned in `module.yml` (part of the module's
  hash, so a model change is a module change and stales approvals).
- **Output:** one Markdown file in `$SWIM_OUT`, recorded as a lane output
  (hashed in the END block and the chain). `writeup` runs `swim observe
  check` on it and logs any invented citation as a WARN.
- **Never read back.** swim doesn't parse the write-up, feed it into
  knowledge, or let it change anything a round does. It's an artifact for
  people and planners: no Claude-based decisions anywhere in a round.
- **The key** comes from the broker. With `secrets: {ANTHROPIC_API_KEY:
  {guard: OBSERVE_ALLOW_WRITEUP}}` in `swim.yml`, the key is released only
  after the guard is approved, so an unattended run sends nothing nobody
  approved.

### Phase two: repo modules

- **`modules/<name>/`** in the repo, committed. `# Modules: ./modules/notify`
  or, with `modules.dir: modules` in `swim.yml`, just `notify` (a repo
  module shadows a built-in only if `swim.yml` says `override: [notify]`).
- **Go repo modules** are built by swim with the operator's Go toolchain
  (the one Go lanes already need), into `.swim/modules/build/<source
  sha>-<goos>-<goarch>`, once per source change. A machine without Go
  can't run them, and `swim doctor` says so.
- **`swim modules new NAME --kind bash|go`** writes a skeleton with a
  `module.yml`, and `swim lint` checks modules too: the bash checks apply to
  bash modules, `module.yml` must parse, `commands:` must exist.

### Phase three: fetched just in time

Like Terraform modules: a source address, a fetch step before anything
runs, and a lockfile. Never a fetch at run time.

```bash
# Modules: github.com/org/swim-modules//pagerduty@2
```

- **`swim modules get`** resolves each source address to an exact version
  (a tag matching the major, or a commit), downloads it, checks its tree
  hash, and stores it content-addressed in `.swim/modules/<tree sha>/`. It
  records the result in `.swim.lock`:
  ```yaml
  modules:
    github.com/org/swim-modules//pagerduty:
      version: 2.3.1
      commit: 9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b2c1d0e
      sha256: 5be1…            # the module tree
      signed_by: zach          # optional: checked against trust.yml
  ```
- **Run time only reads the lock.** A lane naming a fetched module that
  isn't in `.swim/modules/` with the locked hash doesn't start: `module
  pagerduty 2.3.1 isn't here: swim modules get`. A queued or scheduled
  batch pins the module's hash with the rest of its manifest.
- **Changing the lock is a reviewed diff.** `.swim.lock` is committed, so an
  upgrade (`swim modules get --upgrade pagerduty`) shows up in review, and
  stales every approval of a lane that uses the module.
- **Signatures, optional:** a source can publish an SSH signature over each
  version's tree hash. With `modules.require_signed: true` in `swim.yml`,
  only versions signed by a key in `trust.yml` are fetched, the same trust
  `swim verify` uses.
- **Go modules fetched this way** are built locally, as in phase two.
  Prebuilt per-platform binaries (Terraform's provider model, a hash per
  platform in the lock) are an open question.

## Design

| Area | Change |
|---|---|
| `internal/module` (new) | `module.yml` (`swim.module/v1`) parsing and validation; the registry of built-in, repo and fetched modules; resolution of a header's names against `swim.yml` policy and the lock; tree hashing; Go builds into the build cache; bash function-name collision checks. |
| `internal/module/builtin/` | The phase-one modules, embedded (`go:embed` for bash; Go modules compiled in and dispatched by `swim _module NAME`). |
| `internal/lane` | Parse `# Modules:` and `modules:` in `lane.N.yml`. |
| `internal/manifest` | `Pinned.Modules []ModulePin{name, version, source, sha256}`; repo and fetched modules' files staged with the lane; Go binaries under `Local`. Approvals cover it through `manifest_sha256`. |
| `internal/config` | `swim.yml` `modules:` (`allow`, `dir`, `override`, `require_signed`), part of the policy hash. |
| `internal/assets/lib.sh` | `lane_init` sources declared bash modules from the stage; `mod` and `mod_env`, built on the same path as `run`. |
| `internal/cli` | `swim modules [ls|show|new|get]`; hidden `swim _module NAME ...` runs a module command as `swim step` runs a command; `SWIM_MODULE_OUT` handling, masking and the credential-name rule. |
| `internal/cli/lanesock.go`, `proto/swim/lane/v1` | Op `module` (`name`, `args`, `opts`, `env`); the README's op table. SDKs: `module()` in Python, TypeScript and Go. |
| `internal/secrets` | A module's declared `secrets:` go through the broker, refused when the repo doesn't give them to the lane. |
| `internal/lint` | `module` (error: undeclared use, unknown module, not allowed by policy, a bad `module.yml`); `module-effects` (warn: a `read_only: false` module before `stage change`, a `credentials` module used with `mod` instead of `mod_env`). |
| `internal/launcher/plan.go` | Each lane's modules, versions, sources and effects; `--yaml` adds `modules`. |
| Approval prompt, hub | The modules and their effects, next to the guards they come with. |
| `internal/doctor` | Fetched modules missing for the lock, Go repo modules with no toolchain, a lock entry no lane uses. |
| `.swim.lock`, `internal/syntax` | The `modules:` section; a state syntax bump only if the lock's shape requires it (it adds a key, so probably not). |
| [observe.md](observe.md) | Its write-up becomes `writeup`; its "how it calls Claude is the script's business" goes. |
| `internal/help`, README, `swimlane.md` | A MODULES section; `# Modules:` in the header list; the brief's *Integrity* line gains "and the modules it declares". |

### Implementation steps

1. **`internal/module` foundations:** `module.yml`, the built-in registry
   (embedded), tree hashing, name resolution against `swim.yml` policy.
   Unit tests for resolution, policy refusals, and hash stability.
2. **Header and manifest:** `# Modules:`, `Pinned.Modules`, staging of
   module files; a module change stales an approval (e2e).
3. **Bash modules:** `lane_init` loading, `mod`, collision refusal; the
   `clone` and `notify` modules with their tests (a local bare repo for
   clone; a local HTTP server for notify).
4. **Go modules:** `swim _module`, `mod_env` and `SWIM_MODULE_OUT` with
   masking and the credential-name rule; `aws-session` (tested against a
   fake STS endpoint).
5. **Lane protocol:** op `module`, the three SDKs, conformance
   (`sdk/go/conformance`).
6. **Surfaces:** `swim modules ls|show`, plan, the approval prompt, lint.
7. **`writeup`**, after observe.md's phase one exists: observations in,
   masked excerpts, the API call (a stub server in tests), the artifact,
   `swim observe check`.
8. **Phase two:** repo modules, `swim modules new`, Go builds and their
   cache, doctor.
9. **Phase three:** source addresses, `swim modules get`, the lock section,
   signatures, doctor.
10. **Docs** at each phase.

## As built (phases one and two)

Where the build differs from the design above, or settles it:

- **Bash modules aren't sourced into the lane.** Each `mod` call runs the
  module in its own bash (`bash -c '<module>; "$@"'` through `swim _module`
  under `swim step`), so nothing a module defines reaches the lane, and the
  load-time collision check isn't needed. Two declared modules with one
  command name are refused when they resolve.
- **Commands are namespaced:** a module's commands are `<name>` or
  `<name>_<more>`, and `module.yml` must list each one the `.sh` defines.
  A Go module is one command, its name.
- **`swim _module CMD args`** (hidden) resolves CMD among the lane's
  modules as pinned by the staged manifest (a built-in must have the pinned
  hash; a repo module is read from the stage and rehashed), then runs it.
  Run directly with bash, it resolves from the working tree.
- **`mod_env`:** the module writes `KEY=VALUE` lines to
  `$SWIM_MODULE_OUT`; swim refuses `PATH`, `SWIM_*`, the shell's and the
  loader's variables, and credential names (`AWS_*`, `*_TOKEN`, `*_KEY`,
  ...) unless `credentials: true`, then exports them and adds the names to
  `SWIM_ENV_KEYS`, so every later swim process masks them. A value the
  script echoes itself, outside a step, isn't masked (the lane's wrapper
  built its masker before the value existed).
- **Secrets:** `swim secret get` inside a module refuses a name its
  `module.yml` doesn't list; the broker still decides.
- **`aws-session` asks the AWS CLI** (`aws configure export-credentials`,
  `aws sts assume-role`), so it works with any profile (keys, SSO, a
  credential process) and swim needs no AWS SDK.
- **Policy:** `modules:` joined the policy hash as policy version 3, which
  stales every manifest-bound approval once (CHANGELOG Upgrading). Without
  `allow:`, built-ins are allowed and repo modules aren't; with it, only
  what it lists (`./path` globs for repo modules).
- **Go repo modules** build offline (`GOPROXY=off`, `GOFLAGS` cleared) into
  `.swim/modules/build/<tree sha>-<go version>-<os>-<arch>`. The binary
  isn't recorded under `local:` yet: it's keyed by the pinned source hash
  and rebuilt when that or Go changes.

- **More built-ins** (after phase two): `target` (account, context or
  project check), `backup` (RDS, Aurora, DynamoDB restore points; the id as
  an output) and `wait` (HTTP status, TCP, exit code, kube rollout), all Go,
  all through the tools' own CLIs.
- **`tf`, `smoke` and `claude`** (Go). `claude` is the spec's `writeup`
  in another form: it runs the Claude Code CLI (a binary the operator
  names in the call: `-- CMD ARGS...` or `--bin`; never a swim setting,
  so the approved script says what runs) rather than calling the API, with
  no tools, no MCP and a fixed system prompt, from an empty directory, on a
  pinned prompt and masked inputs. It doesn't wait on observe.md: the lane
  passes what it wants written up (`--log`, `--input`). `mod --evidence`
  makes a module call an evidence step, and `module.yml` gained
  `read_only_for` and `destructive_for` (effects per first argument).
- **`gate: true`** in `module.yml`: every failure of the module stops the
  round, as `mod --gate` does. `swim _module` exits 88 for a failed gate
  module; `mod` and op `module` stop the round on it. `target` is one.

Answers to the open questions, for what's built:

- **Remote Go modules:** build, as repo modules do; `swim doctor`
  (`module-go`) flags a lane whose Go module this machine can't build.
  Prebuilt binaries stay open for phase three.
- **Versions in the header:** majors (`clone@1`); `swim plan` and `swim
  approve` show the exact version.
- **Effects enforcement:** no. Lint warns (`module-effects`) about a module
  that isn't `read_only` before `stage change`; `swim plan --check` treats a
  module like any step.
- **Built-in upgrades:** one version of each built-in per swim release for
  now. A release that changes a built-in stales the approvals of lanes that
  use it, and says why ("module notify changed"). Revisit if that's noisy.

## Open questions

- **Remote Go modules: build or download?** (Answered above for what's
  built; still open for prebuilt binaries in phase three.) Building needs a Go toolchain
  on every machine that runs the lane. Prebuilt binaries per platform, each
  hashed in the lock, need none, but the approval then covers a binary
  nobody reviewed, only its source's claim. Leaning: build, and say so in
  `swim doctor`.
- **Versions in the header.** (Answered above.) `notify@1` (a major, with the exact version in
  the lock) or the exact version (`notify@1.2.0`) in every script? Majors
  keep scripts stable across patch releases; exact pins make the script
  self-describing. Leaning majors, with `swim plan` showing the exact one.
- **Effects enforcement.** (Answered above.) Declared effects are advice. Should a lane with
  only `read_only` modules and no `stage change` be allowed to run under
  `swim plan --check` without the usual caution? It's tempting, and wrong
  if a module lies. Leaning no.
- **Built-in module upgrades.** (Answered above, for now.) A swim release that changes `notify` stales
  every approval of a lane using it, which is correct but noisy. Should
  built-in modules be versioned independently of swim, with old versions
  kept in the binary for a release or two?
- **Where `writeup` stops.** Run reports are the use case. Should it also
  write per-lane notes, or a knowledge proposal file for `swim observe
  accept`? Keep it to one artifact per call until there's demand.

## Testing

- **Unit, resolution:** built-in, repo and fetched names; `@major`; a name
  the policy doesn't allow; a repo module shadowing a built-in without
  `override`.
- **Unit, `module.yml`:** a missing field, an unknown effect, a command that
  isn't defined, a bash function that shadows a library function.
- **e2e, pinning:** a lane using a repo module passes; editing the module
  stales its signed approval; a queued batch pinned before the edit is
  skipped as changed.
- **e2e, undeclared:** `mod notify` without `# Modules: notify` is refused
  at the step and by lint.
- **e2e, `mod_env`:** a stub credentials module's values reach later steps
  and are masked in the log; a non-credentials module writing
  `AWS_SECRET_ACCESS_KEY` is refused.
- **e2e, built-ins:** `clone` refuses a branch name and checks out the SHA;
  `notify` posts to a local server with the secret from the broker, and
  fails soft when it's down; `aws-session` against a fake STS.
- **e2e, SDK:** a Python and a Go lane call `notify` through op `module`.
- **e2e, `writeup`:** against a stub API server: the request has no tools,
  carries no unmasked secret, and the artifact lands as an output; without
  the guard approved, the key is refused and nothing is sent.
- **Phase three:** `swim modules get` writes the lock; a lane whose fetched
  module is missing doesn't start; a tampered module tree is refused; an
  unsigned version is refused under `require_signed`.

## Out of scope

- Running GitHub Actions (`uses:`) or emulating them. Modules may cover the
  same needs, written for swim.
- Modules in Python or TypeScript. SDK lanes in those languages call Go
  and bash modules through the lane protocol.
- A module registry server. Phase three fetches from git hosts by source
  address.
- Modules that decide what a round does, Claude-based or otherwise. A
  module is a step; the script decides what runs.
- Sandboxing modules. They run as the lane, with its permissions; scoped
  credentials and OS isolation are the brief's answer to that, not this.
