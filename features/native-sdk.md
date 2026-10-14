# Native SDKs over a lane protocol

Status: shipped (unreleased)

## Summary

Libraries for Python, TypeScript and Go that give lane scripts swim's step
functions (`run`, `gate`, `snapshot`, `guard`, `confirm`, `stage`, `drift`,
`stop`) as native calls, instead of a subprocess per call. They're generated
from one protobuf contract, `swim.lane.v1`, and talk to swim over a per-lane
Unix socket. All the rules stay in the Go binary: logging, masking, approval
decisions, timeouts and the chain. The SDKs are thin clients.

```python
import boto3, swim

with swim.round() as r:
    r.stage("snapshot")
    r.snapshot("cfn template", ["aws", "cloudformation", "get-template", "--stack-name", "orders-prod"])
    r.stage("check")
    r.gate("tf plan", ["terraform", "-chdir=infra/orders", "plan", "-detailed-exitcode"])
    with r.step("orders table is empty", gate=True) as s:      # runs in-process
        n = boto3.client("dynamodb").scan(TableName="orders", Select="COUNT")["Count"]
        s.log(f"items: {n}")
        s.require(n == 0, "table still has items")
    r.stage("change")
    if r.guard("ORDERS_ALLOW_DROP", "drop orders table (tf owns it now)"):
        r.run("drop table", ["aws", "dynamodb", "delete-table", "--table-name", "orders"])
```

## Motivation

- **Polyglot lanes already exist, but each call is a process.** A
  `lane.N.py` runs inside `swim _wrap` (`internal/cli/wrap.go`) and calls the
  in-lane commands (`swim gate`, `swim guard`, ...,
  `internal/cli/lanecmds.go`) as subprocesses. That costs a process per
  step, passes everything as argv strings, and reports failures as exit
  codes and signals.
- **Native checks are awkward.** The reason to write a lane in Python or
  TypeScript is to call `boto3` or the AWS SDK directly. Today a native check
  has to be shelled out, or it can't be a logged step at all.
- **Errors aren't idiomatic.** A failed gate ends the round by signalling the
  wrapper (SIGUSR1), which kills the program. An SDK can raise a proper
  exception and let the program unwind cleanly first.
- **The protocol is implicit.** In-lane commands, `_mark`, `_approve` and
  `_finish` already form a protocol, spread across argv, env vars,
  `.swim/laneN.end` and signals. Writing it down makes it testable and
  versioned, and makes new languages cheap to add.

## Behaviour

### What stays in the binary

The SDK never decides anything that affects safety or the record:

- **Approvals:** `Guard` asks swim, which applies flags, signed approvals,
  `lookup.yml`, the terminal prompt (through the live view), `-y` and queued
  approvals. The SDK only gets the answer back.
- **Logging and masking:** all output is masked and written by swim. The SDK
  never writes to `.swim/logs`.
- **Running commands:** `run`, `gate` and `snapshot` with an argv are
  executed by swim exactly as `swim step` does today: timeouts, retries,
  blocked commands, the git shim and output capture.
- **The round itself:** the wrapper still opens and closes it, writes the
  chain entry, and kills the program if it ignores an end.

### Two kinds of step

| | Executed by swim (`r.run(label, argv)`) | In-process (`with r.step(label)`) |
|---|---|---|
| Who runs it | swim, as a child process | the lane program itself |
| Output in the log | captured by swim | whatever the SDK sends (`s.log`, captured stdout and stderr) |
| Timeouts and retries | enforced by swim | the deadline is sent to the SDK, which cancels; past a grace period, the wrapper kills the program |
| Blocked commands | enforced (argv check and git shim) | only for subprocesses the program spawns (the git shim is on `PATH`) |
| Log marker | as today | `PASS  <label> (in-process)` |

- **The default:** steps executed by swim are the default and the
  recommended form for anything destructive.
- **What in-process steps are for:** checks and reads where a native client
  is the point. They're honest about having weaker guarantees, rather than
  pretending to have the same ones.
- **Gates:** an in-process step with `gate=True` is a gate. Its failure ends
  the round.

### Ending the round

- **When it happens:** a failed gate, `stop`, a refused step, an operator
  stop or a round timeout makes the SDK raise `swim.RoundEnded`. In Python
  it derives from `BaseException`, so `except Exception` doesn't swallow it.
  In TypeScript it's a distinct error class.
- **Unwinding:** the program unwinds, `finally` and `with` blocks run, and
  `swim.round()` closes. If the program doesn't exit within the grace
  period, the wrapper kills it as today.
- **Handlers:** with [outcome-handlers.md](outcome-handlers.md), handlers are
  registered with decorators (`@r.on_failure`) and run by `swim.round()` as
  it closes. The wrapper's grace period becomes the round's
  `Handler-Timeout`.

### Outside swim

The SDK refuses to run without `SWIM_SOCKET`. Like `lane_init`, a round only
exists when swim starts it. The message says to run it with `swim run N`.

## Design

### Contract

- **Where it lives:** `proto/swim/lane/v1/lane.proto`, managed with `buf`.
  Generated code is committed, and `buf breaking` runs in CI against
  `main`.
- **Service sketch:**

  | RPC | Does |
  |---|---|
  | `Hello` | handshake: protocol version, swim version, breaking number, lane, job, run, deadline |
  | `Stage(name)` | start a stage |
  | `Run(label, argv, mode, opts)` → stream | execute a step (`run`, `gate`, `snapshot`); streams masked output, then the result |
  | `BeginStep(label, gate)` / `StepOutput(id, chunk)` / `EndStep(id, ok, exit, message)` | an in-process step |
  | `Guard(flag, action)` | approved, or declined as a dry run, and how |
  | `Confirm(question)` | yes or no; fail-closed |
  | `Drift(what)`, `Stop(reason)` | as today |
  | `Events()` → stream | round-ended and operator-stop notices, so the SDK can raise between calls |

- **Versioning:** the package is `swim.lane.v1`, and `Hello` checks
  compatibility. Fields only grow within `v1`, the same rule as the YAML
  schemas. A breaking change becomes `v2`, and swim serves both for one
  breaking version.

### Server

- **Where:** `swim _wrap` already holds each wrapped lane's lifecycle. It
  serves the socket and implements each RPC with the same code the in-lane
  commands use. Those commands become thin callers of the same functions, so
  there's one implementation.
- **Transport:** gRPC over a Unix socket. The path is short, under
  `$TMPDIR/swim-<run>/<lane>.sock`, because macOS limits socket paths to 104
  bytes. The socket is mode `0600`.
- **Authentication:** a random per-lane token in `SWIM_TOKEN`, checked on
  every call, so another lane or local process can't act as this lane.
- **Concurrency:** one round per connection, and calls are serialized per
  lane. Steps in a round stay sequential, as in bash.

### SDKs

- **Layout:**
  - `sdk/python`: generated stubs plus a small hand-written layer (context
    managers, `RoundEnded`, `async` variants).
  - `sdk/ts`: Node, Bun and Deno.
  - `sdk/go`: for compiled lanes.
- **Shared behaviour:** each SDK reads `SWIM_SOCKET` and `SWIM_TOKEN`, calls
  `Hello`, and maps results to idiomatic types.
- **Releases:** SDK versions follow swim's (`<breaking>.<YYYYMMDD>`).
  `make release` publishes all of them from the same tag.

### Chain of custody

- **The gap:** an approval binds to the script's SHA-256. A `lane.N.py`
  that imports local modules would be approved without them.
- **Proposed fix:** a header `# Includes: lib/orders/*.py` lists repo files
  that are part of the round. They're hashed into the round's script hash and
  kept under `.swim/scripts/`. Lint warns about local imports not covered,
  on a best-effort basis for Python and TypeScript.

### Lint

- **Python and TypeScript lanes:** check the header and `Includes`, and flag
  `subprocess` or `child_process` calls that bypass `r.run`.
- **Not in scope:** full static analysis.

## Open questions (answered)

1. **Package names:** `swim-lanes` on PyPI (imported as `swim`) and
   `@gates-brightly/swim` on npm. The Go package is
   `github.com/gates-brightly/swimlane/sdk/go/swim`.
2. **`Includes` or single-file rounds:** `# Includes:` (globs). The files
   are hashed with the script (`lane.ScriptBytes`), and lint warns about a
   local import they don't cover.
3. **Bash lanes:** they stay on the subprocess path (the library), which
   already works everywhere.
4. **Transport:** JSON lines over the Unix socket, not gRPC. The contract
   (`proto/swim/lane/v1/README.md`) is the same set of calls the spec
   sketched, and the SDKs have no dependencies (`grpcio` would weigh more
   than the Python SDK itself).
5. **Capturing stdout:** only what `s.log` sends. The program's own stdout
   still reaches the terminal and the wrapper.

Decided while building:

- **One implementation:** a run-type call is the in-lane command (`swim run
  "<label>" -- ...`) run by the wrapper with the lane's env. Guards, stages,
  messages, inputs and outputs work the same way, so the SDKs can't drift
  from the CLI.
- **Grace:** with an SDK connected, an ended round gives the program 10s to
  unwind (`RoundEnded`, finally blocks) before the wrapper stops it.
- **Confirm** asks through the approval path, with flag `SWIM_CONFIRM`,
  which nobody can preset. No ends the round.
- **Handlers** registered with the SDK run as the round closes, inside
  `handler` sections, soft.
- **Not done:** publishing the SDKs from `make release`, and `buf breaking`
  (there's no protobuf). The conformance test runs bash, Python, TypeScript
  (when node can strip types) and Go against one round.

## Testing

- **Conformance:** one set of scenario rounds, written in bash, Python,
  TypeScript and Go, must produce the same parsed log (`swim log --yaml`,
  apart from runner and `(in-process)` markers). It runs in
  `e2e/scenarios`.
- **Approvals:** `Guard` gives the same decision as bash for each approval
  source. An SDK client can't approve anything by itself.
- **Ending the round:** a failed gate raises `RoundEnded`, `finally` runs,
  and a program that swallows the exception and keeps going is killed after
  the grace period.
- **Timeouts:** an in-process step past its deadline is cancelled, then
  killed.
- **Security:** a wrong or missing token is refused, another lane's socket
  is refused, and the socket mode is `0600`.
- **Versioning:** a v1 SDK against a swim with a different breaking number
  fails at `Hello` with an upgrade message. `buf breaking` passes.
- **Masking:** secrets sent through `s.log` are masked in logs, YAML and the
  hub.

## Out of scope

- **SDKs that work without swim**, or that reimplement approvals, logging or
  the chain.
- **Remote lanes:** the socket is local. Running a lane on another machine
  stays the queue and worker's job.
- **Read APIs** (status, log, plan): those are for planners and tools, which
  already have `--yaml`.
