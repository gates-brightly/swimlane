# swim.lane/v1: the lane protocol

How a lane program talks to swim through a native SDK
(`sdk/python`, `sdk/ts`, `sdk/go`). swim decides everything (logging,
masking, approvals, timeouts, the chain); the SDKs are thin clients.

## Transport

- `swim _wrap` (which runs every lane that doesn't load the bash library)
  listens on a Unix socket for its lane: `SWIM_SOCKET`, mode `0600`, in a
  short temp directory.
- Every request carries `SWIM_TOKEN`, a random per-lane token. A wrong or
  missing token is refused.
- Requests and responses are JSON objects, one per line. Calls are
  answered in order, one at a time per lane.
- Without `SWIM_SOCKET` an SDK refuses to run: a round exists only when swim
  starts it (`swim run N`).

## Request

```json
{"id": 1, "token": "…", "op": "run", "args": {"label": "tf plan", "argv": ["terraform", "plan"], "opts": ["--timeout", "5m"]}}
```

## Response

```json
{"id": 1, "ok": true, "result": {"exit": 0, "passed": true}}
{"id": 2, "ok": true, "round_ended": true, "code": 1, "result": {"exit": 1, "passed": false}}
{"id": 3, "ok": false, "error": "bad stage nope"}
```

- `ok: false` is a usage error (raise it).
- `round_ended: true` means the round is over: a failed gate or evidence step,
  `stop`, a refused step, an operator stop or a time limit. `code` is its exit
  code. The SDK raises `RoundEnded` (in Python it derives from
  `BaseException`, so `except Exception` doesn't swallow it). The program
  then unwinds (its `finally` blocks run) and exits. If it doesn't within 10
  seconds, swim stops it.

## Ops

| op | args | result |
|---|---|---|
| `hello` | `protocol` (`swim.lane/v1`), `sdk` | `protocol`, `swim`, `breaking`, `lane`, `cell`, `job`, `run`, `deadline` (epoch, 0 for none), `check` (a read-only check) |
| `stage` | `name` | — |
| `run`, `gate`, `snapshot`, `evidence` | `label`, `argv`, `opts` (as for `swim run`: `--timeout D`, `--retry N`, `--backoff D`, `--retry-on CODES`, `--no-retry-timeout`, `--soft`) | `exit`, `passed` |
| `begin_step` | `label`, `gate` | `step` (id), `started` (false: the lane is stopping) |
| `step_output` | `step`, `text` | — |
| `end_step` | `step`, `ok`, `message` | `exit`, `passed` |
| `guard` | `flag`, `action` | `approved` |
| `confirm` | `question` | `confirmed` (no ends the round) |
| `drift` | `what` | — |
| `stop` | `reason` | — (the round ends) |
| `msg` | `text`, `progress` (`N/M`) | — |
| `input` | `ref`, `name` | `value` (a path, or a small value) |
| `output` | `key`, `value` | — |
| `outcome` | — | `outcome` (pass, fail, stopped, interrupted, timeout), `stop_reason`, `failed_step`, `failed_stage` |
| `handler` | `name` (`on_success`, `on_failure`, `on_exit`; empty to leave) | — |

## Semantics

- **Steps run by swim** (`run`, `gate`, `snapshot`, `evidence`) are exactly
  the in-lane commands: same timeouts, retries, blocked commands, git shim
  and output capture.
- **In-process steps** (`begin_step`, `step_output`, `end_step`) are checks
  the program runs itself, such as a native API call. Their block is logged
  as `PASS  <label> (in-process)` with the output the SDK sent. A failed
  in-process gate ends the round. Blocked commands only apply to processes
  the program spawns, since the git shim is on `PATH`.
- **Handlers:** when the program's round closes, the SDK asks `outcome`, and
  runs its registered handler functions inside `handler` sections, which run
  soft: nothing in them ends the round.

## Secrets broker

Secrets (`swim secret`) come from a separate socket that the swim run
serves to all of its lanes, so they work with or without a lane
connection:

- **Transport:** JSON lines on the Unix socket in `SWIM_SECRETS`, one
  request per connection.
- **Request:** `{"token": $SWIM_SECRETS_TOKEN, "op": "get", "name": "db_password", "lane": $SWIM_LANE, "how": "sdk"}`.
- **Response:** `{"ok": true, "value": "...", "scope": "repo|all|env"}`, or
  `{"ok": false, "error": "no secret db_password (...)"}`.
- **Outside a run** (`SWIM_SECRETS` unset), an SDK runs
  `swim secret get NAME`, which decrypts with the operator's key.
- **Masking and audit:** every lane process masks all of the run's secrets
  whatever it fetched, and `.swim.log` records each get by name.

## Versioning

- Fields only grow within `v1`, the same rule as the YAML schemas.
- `hello` checks the protocol. A different major is refused with an upgrade
  message.
- A breaking change becomes `v2`, and swim serves both for one breaking
  version.
