# Go lanes

A Go lane is a compiled program saved as `lane.N`, with its header in a
sidecar file `lane.N.yml`. swim runs the binary inside a round, so the
program gets swim's steps, guards, logging and chain. There are two ways
to talk to swim from Go:

- **The SDK** (`github.com/gates-brightly/swimlane/sdk/go/swim`): steps are
  method calls, checks can run in-process, and handlers are funcs.
- **The CLI**: call `swim run "<label>" -- cmd` with `os/exec`. It needs no
  dependency.

[← back to the README](../../README.md#lanes-in-other-languages) ·
[Python](python.md) · [Node](node.md)

## Setup

Keep the lane's source in its own module and build it to `lane.N` at the
repo root. `swim init` already gitignores `lane.[0-9]*`, so the binary and
its sidecar stay local, and the source is what you commit:

```sh
mkdir -p lanes/rotate && cd lanes/rotate
go mod init example.com/lanes/rotate
go get github.com/gates-brightly/swimlane/sdk/go/swim
# write main.go (below), then:
go build -o ../../lane.3 .
```

swim hashes the binary itself, so a rebuild is a new script: approvals
and the chain see the change.

## With the SDK

This lane rotates the app's signing key. It snapshots the key folder,
records the plan as evidence, checks the current key in Go, and swaps the
key only behind a guard. If a later step fails, it puts the old key back.

`lane.3.yml`:

```yaml
round: Rotate the app's signing key
guards:
  ALLOW_ROTATE: replace keys/current with a new key
```

`lanes/rotate/main.go`:

```go
// Command rotate is lane 3: rotate the app's signing key.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/gates-brightly/swimlane/sdk/go/swim"
)

func main() {
	os.Exit(swim.Round(func(r *swim.R) error {
		r.OnFailure(func(outcome map[string]any) error {
			if _, err := os.Stat("keys/current.bak"); err != nil {
				return nil // failed before the backup: nothing to undo
			}
			_, err := r.Run("restore the old key", "cp", "keys/current.bak", "keys/current")
			return err
		})

		if err := r.Stage("snapshot"); err != nil {
			return err
		}
		if _, err := r.Snapshot("keys before", "ls", "-l", "keys"); err != nil {
			return err
		}

		if err := r.Stage("check"); err != nil {
			return err
		}
		// What will change: an approval covers this output, not just the program.
		if _, err := r.Evidence("rotation plan", "sh", "-c", "echo rotate: keys/current; wc -c < keys/current"); err != nil {
			return err
		}
		err := r.Step("current key is well formed", true, func(s *swim.S) error {
			data, err := os.ReadFile("keys/current")
			if err != nil {
				return err // recorded as the step's failure
			}
			key := strings.TrimSpace(string(data))
			s.Log(fmt.Sprintf("key length: %d", len(key)))
			if len(key) < 16 {
				return s.Fail("the current key is too short")
			}
			return nil
		})
		if err != nil {
			return err
		}

		if err := r.Stage("change"); err != nil {
			return err
		}
		ok, err := r.Guard("ALLOW_ROTATE", "replace keys/current with a new key")
		if err != nil || !ok {
			return err // a dry run ends here
		}
		steps := [][]string{
			{"back up the key", "cp", "keys/current", "keys/current.bak"},
			{"write a new key", "sh", "-c", "head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \\n' > keys/current"},
		}
		for i, st := range steps {
			if err := r.Msg(st[0], fmt.Sprintf("%d/%d", i+1, len(steps))); err != nil {
				return err
			}
			if _, err := r.Gate(st[0], st[1:]...); err != nil {
				return err
			}
		}

		if err := r.Stage("verify"); err != nil {
			return err
		}
		_, err = r.Gate("the key changed", "sh", "-c", "! cmp -s keys/current keys/current.bak")
		return err
	}))
}
```

Build it and run it:

```sh
(cd lanes/rotate && go build -o ../../lane.3 .)
swim run 3                    # a dry run: checks, then the guard skips
ALLOW_ROTATE=1 swim run 3     # or approve when the live view asks
```

The dry run's log, from `swim log 3`:

```
-- stage check  10:55:02
  PASS  evidence: rotation plan                 0.0s  10:55:02
        $ sh -c 'echo rotate: keys/current; wc -c < keys/current'
        | rotate: keys/current
        | 21
        saved: .swim/snapshots/lane3-20261014T105502Z-rotation-plan.txt
  PASS  current key is well formed (in-process)    0.0s  10:55:02
        $ (in-process)
        | key length: 20

-- stage change  10:55:02
  SKIP  replace keys/current with a new key (dry run: set ALLOW_ROTATE=1 to approve)

== END PASS  pass=3 fail=0 skip=1 drift=0  exit=0  0.1s  2026-10-14T10:55:02Z
   stages: snapshot PASS | check PASS | change SKIP | verify none
```

With a key that's too short, the in-process gate stops the round before
anything changes, and the handler finds no backup to restore:

```
  FAIL  current key is well formed (in-process, the current key is too short)
        | key length: 3
  STOP  gate failed: current key is well formed
```

Because the check stage records evidence, `swim approve 3` shows the
rotation plan and signs it with the binary. The approval then holds only
while the plan still matches (see
[Guards and approvals](../../README.md#guards-and-approvals)).

### What each call does

| Call | What it does |
| --- | --- |
| `os.Exit(swim.Round(func(r *swim.R) error { ... }))` | Opens the round, runs the handlers when it ends, and returns the exit code. |
| `r.Stage(name)` | Starts a stage: `snapshot`, `check`, `change` or `verify`. |
| `r.Run(label, argv...)` | A step that swim runs and logs. Returns `true` if it passed; a failure doesn't stop the round. |
| `r.RunWith(label, swim.Opts{...}, argv...)` | `Run` with `Timeout`, `Retry`, `Backoff`, `RetryOn` and `Soft`. |
| `r.Gate(label, argv...)` | A step whose failure ends the round. |
| `r.Snapshot(...)` / `r.Evidence(...)` | A read-only step whose output is kept, or whose output an approval covers (the plan). |
| `r.Step(label, gate, func(s *swim.S) error { ... })` | An in-process step. `s.Log(text)` adds output lines (masked), and returning `s.Fail(msg)`, or any error, fails it. |
| `r.Guard(FLAG, action)` | `true` if the operator approves; `false` is a dry run. |
| `r.Confirm(question)` | `true` on yes; no ends the round. |
| `r.Msg(text, "n/m")` | The lane's status message, in the live view and `swim status` (`""` for no progress). |
| `r.Input(lane, name)` / `r.Output(key, value)` | Read a dependency's output (a path, or a small value), or set one of your own. |
| `r.Drift(what)` / `r.Stop(reason)` | Record drift, or stop the round. |
| `r.OnSuccess(fn)` / `r.OnFailure(fn)` / `r.OnExit(fn)` | Handlers that get the outcome map (`outcome`, `stop_reason`, `failed_step`, `failed_stage`). Register them before the first step. |

### Things to know

- **Return every error.** When the round ends (a failed gate, `r.Stop`, an
  operator stop or a time limit), the next call returns a
  `*swim.RoundEnded`. Return it, and `swim.Round` exits with its code. Use
  `errors.As` if you need to tell it apart. Deferred funcs still run; you
  get 10 seconds before swim stops the program.
- **The sidecar is the header.** `lane.N.yml` takes the same keys as a
  script header: `round`, `job`, `after`, `guards`, `locks`, `timeout`,
  `step-timeout`, `outputs`, `matrix`, `fail` and the `on-*` handler
  commands.
- **`Soft` means "don't end a `fail: hard` round".** It doesn't stop a
  failed step from counting.

## Without the SDK

Any program can call the swim CLI. Each call is one step, and its exit
code is the step's result:

```go
package main

import (
	"os"
	"os/exec"
)

func swim(args ...string) bool {
	cmd := exec.Command("swim", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run() == nil
}

func main() {
	swim("stage", "check")
	swim("gate", "snapshots exist", "--", "test", "-d", ".swim/snapshots")
	swim("stage", "change")
	if swim("guard", "ALLOW_TIDY", "delete snapshots older than a week") {
		swim("run", "delete old snapshots", "--", "find", ".swim/snapshots", "-mtime", "+7", "-delete")
	}
}
```

Give it a sidecar with `round:` and `guards:`. A failed `gate` ends the
program for you.
