# T43. Stream running lanes' logs to the hub, where the repo allows it (P1, hub, gap)

Part of [swim.plan.md](../swim.plan.md) Phase 2. Needs T42.

**Today:** a running lane sends the last 15 lines of its log in each
snapshot (`laneTail`, `internal/cli/hub.go`). The whole log reaches the
hub only when a user presses Log, on a headless worker whose `swim.yml`
allows it (`headless.logs`, `config/headless.go`), and stays in memory 15
minutes (`internal/hub/logs.go`).

**Do:**
- `swim.yml`: a `hub:` block with `logs: stream | request | off`
  (default `request`, today's behavior). It applies to every worker in the
  repo, interactive or headless; `headless.logs: off` still means off.
  Only the committed file counts, as for `headless:`.
- Worker, under `stream`: for each running lane, `PUT
  /api/workers/{id}/logs/{run}/{lane}?offset=N` with the bytes past N,
  masked, every report tick. The hub answers 409 with its offset when they
  disagree; the worker resends from there. When the round ends, one last
  append and a `final` flag.
- Hub: append to `--state DIR/runs/<worker>/<run>/lane.N.log` (the same
  retention as T42), sanitized line by line (`ui.SafeLines`). Reading
  needs a viewer login, as now. The in-memory cache stays for `request`.
- The snapshot says which mode the repo is in, so the app knows whether
  to offer Log or show the stream.

**Done when:** with `hub: {logs: stream}`, a 3-step lane's log on the hub
matches `.swim/logs/` byte for byte (after masking) while it runs and once
it ends; a dropped append is recovered by offset; with the default, the
hub gets no log until asked; with `off`, never (tests for each, and a
secret in the output arrives masked).
