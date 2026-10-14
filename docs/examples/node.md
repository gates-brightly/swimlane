# Node lanes (TypeScript and JavaScript)

A Node lane is a file named `lane.N.ts`, `lane.N.mjs` or `lane.N.js`. swim
runs it with its `#!` line, or with the runner `swim.yml` names for the
extension, inside a round. There are two ways to talk to swim from Node:

- **The SDK** (`sdk/ts/swim.ts`): steps are `await`ed calls, checks can run
  in-process, and handlers are callbacks. It has no dependencies, and its
  types are erasable, so Node runs it without a build step.
- **The CLI**: call `swim run "<label>" -- cmd` with `child_process`. It
  needs no dependency.

[← back to the README](../../README.md#lanes-in-other-languages) ·
[Python](python.md) · [Go](go.md)

## Setup

Tell swim how to run `.ts` files, once per repo, in `swim.yml`:

```yaml
runners:
  ts: node --experimental-strip-types --no-warnings   # Node 22.6+; or: tsx, bun, deno run -A
```

Then copy the SDK next to your lanes:

```sh
mkdir -p lib && cp path/to/swimlane/sdk/ts/swim.ts lib/swim.ts
```

Node won't strip types from `.ts` files under `node_modules`, so an
installed `@gates-brightly/swim` works with tsx, bun and deno, but not with
plain `node --experimental-strip-types`. A vendored copy works with all of
them. List it under `Includes:` so it's hashed with the script, and an
approval covers it too.

## With the SDK

This lane runs after [lane 1](python.md#with-the-sdk) and publishes that
lane's report. It reads the report through `input`, checks it in Node,
publishes behind a guard, and undoes the publish if a later step fails.

```ts
// swim: syntax 3
// Round:    Publish the archive report
// After:    1
// Includes: lib/swim.ts
// Guards:   ALLOW_PUBLISH  copy the report to the shared reports/ folder
import { readFileSync } from "node:fs";
import { round } from "./lib/swim.ts";

await round(async (r) => {
  r.onFailure(async (outcome) => {
    await r.run(`undo publish (${outcome.outcome})`, ["rm", "-f", "reports/archive.json"]);
  });

  await r.stage("snapshot");
  await r.snapshot("reports before", ["sh", "-c", "ls -l reports 2>/dev/null || echo none yet"]);

  await r.stage("check");
  const path = await r.input(1, "report.txt");          // lane 1's output file
  const report = JSON.parse(readFileSync(path, "utf8"));
  await r.step("report lists tables", async (s) => {    // runs here, in Node
    s.log(`tables: ${report.tables.join(", ")}`);
    s.require(report.tables.length > 0, "the report is empty");
  }, { gate: true });

  await r.stage("change");
  if (!(await r.guard("ALLOW_PUBLISH", "copy the report to the shared reports/ folder"))) {
    return;                                             // a dry run ends here
  }
  await r.run("make reports/", ["mkdir", "-p", "reports"]);
  // Inputs are read-only; install gives the copy its own mode.
  await r.run("publish", ["install", "-m", "0644", path, "reports/archive.json"], { retry: 2, backoff: "1s" });

  await r.stage("verify");
  await r.gate("report is published", ["test", "-s", "reports/archive.json"]);
  await r.output("tables", String(report.tables.length));
});
```

Save it as `lane.2.ts` and run both lanes. Lane 2 waits for lane 1:

```sh
swim run 1 2
```

The approved round's log, from `swim log 2`:

```
-- stage check  10:55:20
  PASS  report lists tables (in-process)        0.0s  10:55:20
        $ (in-process)
        | tables: orders, billing, users

-- stage change  10:55:20
  APPROVED  copy the report to the shared reports/ folder (ALLOW_PUBLISH=1)
  PASS  make reports/                           0.0s  10:55:20
        $ mkdir -p reports
  PASS  publish                                 0.0s  10:55:20
        $ install -m 0644 .swim/out/.../report.txt reports/archive.json
        -- attempt 1/3
        -- exit 0 (0.0s)

-- stage verify  10:55:20
  PASS  report is published                     0.0s  10:55:20
        $ test -s reports/archive.json

== END PASS  pass=5 fail=0 skip=0 drift=0  exit=0  0.2s  2026-10-14T10:55:20Z
   stages: snapshot PASS | check PASS | change PASS | verify PASS
   outputs: value:tables 1 B 4e07408562be
```

The input and the output are hashed into the chain, so `swim verify`
proves this lane published exactly what lane 1 produced.

### What each call does

| Call | What it does |
| --- | --- |
| `await round(async (r) => { ... })` | Opens the round. When it ends, `round` runs the handlers and exits with the round's code. |
| `await r.stage(name)` | Starts a stage: `snapshot`, `check`, `change` or `verify`. |
| `await r.run(label, argv, opts?)` | A step that swim runs and logs. Resolves `true` if it passed; a failure doesn't stop the round. |
| `await r.gate(label, argv, opts?)` | A step whose failure ends the round. |
| `await r.snapshot(...)` / `await r.evidence(...)` | A read-only step whose output is kept, or whose output an approval covers (the plan). |
| `await r.step(label, async (s) => { ... }, { gate })` | An in-process step. `s.log(text)` adds output lines (masked), and `s.require(ok, msg)` fails it. Any other exception fails it too and is rethrown. |
| `await r.guard(FLAG, action)` | `true` if the operator approves; `false` is a dry run. |
| `await r.confirm(question)` | `true` on yes; no ends the round. |
| `await r.msg(text, [n, m])` | The lane's status message, in the live view and `swim status`. |
| `await r.input(lane, name)` / `await r.output(key, value)` | Read a dependency's output (a path, or a small value), or set one of your own. |
| `await r.drift(what)` / `await r.stop(reason)` | Record drift, or stop the round. |
| `await secret.get(name)` | A secret stored with `swim secret set` (`import { secret }`). swim masks it as `***(name)` everywhere it writes. |
| `r.onSuccess(fn)` / `r.onFailure(fn)` / `r.onExit(fn)` | Handlers that get `outcome` (`outcome`, `stop_reason`, `failed_step`, `failed_stage`). Register them before the first step. |

The step options are `{ timeout: "5m", retry: 2, backoff: "1s", retryOn: [75], soft: true }`.

### Things to know

- **Await every call.** Calls go to swim one at a time, in order, so an
  un-awaited `r.run` still runs, but your code races ahead of it.
- **Let `RoundEnded` go.** A failed gate, `r.stop`, an operator stop or a
  time limit throws `RoundEnded`. Don't catch it, or rethrow it if you do,
  so `finally` blocks run; you get 10 seconds before swim stops the
  program.
- **`soft: true` means "don't end a `Fail: hard` round".** It doesn't stop
  a failed step from counting. For a snapshot of something that may not
  exist yet, make the command itself succeed.
- **Plain JavaScript** works the same way: name the file `lane.N.mjs`,
  give it a `#!/usr/bin/env node` line, and import `./lib/swim.ts`. Node
  23.6 and later strip its types by default.

## Without the SDK

Any program can call the swim CLI. Each call is one step, and its exit
code is the step's result. This `lane.4.mjs` needs nothing but Node:

```js
#!/usr/bin/env node
// Round:  Tidy old snapshots
// Guards: ALLOW_TIDY  delete snapshots older than a week
import { spawnSync } from "node:child_process";

const swim = (...args) => spawnSync("swim", args, { stdio: "inherit" }).status === 0;

swim("stage", "check");
swim("gate", "snapshots exist", "--", "test", "-d", ".swim/snapshots");
swim("stage", "change");
if (swim("guard", "ALLOW_TIDY", "delete snapshots older than a week")) {
  swim("run", "delete old snapshots", "--", "find", ".swim/snapshots", "-mtime", "+7", "-delete");
}
```

Use `spawnSync` (or `await` each child), not fire-and-forget `spawn`:
steps must run one at a time. A failed `gate` ends the program for you.
Handlers for CLI lanes are commands in the header, with
`// swim: syntax 3` and `// On-Failure: node rollback.mjs`.
