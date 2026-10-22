# T43. Stream running lanes' logs to the hub, where the repo allows it (P1, hub, gap)

Part of [swim.plan.md](../swim.plan.md) Phase 2.

**Today:** a running lane sends the last 15 lines of its log in each
snapshot (`laneTail`, `internal/cli/hub.go`). The whole log reaches the
hub only when a user presses Log, on a headless worker whose `swim.yml`
allows it (`headless.logs`, `config/headless.go`), and stays in memory 15
minutes (`internal/hub/logs.go`).

Since T42, `swim.yml`'s `hub:` block has `logs: request | stream | off`
(`config/hub.go`, `cfg.HubLogs()`): under `stream` a finished run's report
carries its step output (`internal/cli/hubruns.go`), and finished runs are
kept on disk (`internal/hub/runs.go`).

**Do:**
- Worker, under `stream`: for each running lane, `PUT
  /api/workers/{id}/logs/{run}/{lane}?offset=N` with the bytes past N,
  masked, every report tick. The hub answers 409 with its offset when they
  disagree; the worker resends from there. When the round ends, one last
  append and a `final` flag.
- Hub: append to `--state DIR/runs/<worker>/<run>.lane.N.log`, dropped
  with its run by the runs' retention (`runStore.drop`), sanitized line by line (`ui.SafeLines`). Reading
  needs a viewer login, as now. The in-memory cache stays for `request`.
- The snapshot says which mode the repo is in, so the app knows whether
  to offer Log or show the stream.

**Done when:** with `hub: {logs: stream}`, a 3-step lane's log on the hub
matches `.swim/logs/` byte for byte (after masking) while it runs and once
it ends; a dropped append is recovered by offset; with the default, the
hub gets no log until asked; with `off`, never (tests for each, and a
secret in the output arrives masked).
