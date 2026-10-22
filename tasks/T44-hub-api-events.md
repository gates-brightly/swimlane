# T44. A read API for the app: enrollments, runs, jobs and events (P1, hub, gap)

Part of [swim.plan.md](../swim.plan.md) Phase 2. Logs need T43.

**Today:** `GET /api/workers` (`swim.hub/v1`) is the one read endpoint,
the latest snapshot of each worker; pages poll by reloading.

swim is pre-release: change the API in place, no `/v2` and no
compatibility shims. Update the schema string and every caller (the
admin CLI's `Admin.Workers`, `internal/hub/client.go`) in the same change.

**Do:** read-only, JSON, viewer login under `--private` (logs always):

| Route | Gives |
|---|---|
| `GET /api/enrollments` | workers by namespace: name, host, state, counts, stale, pending approvals |
| `GET /api/enrollments/{id}` | one worker's snapshot (lanes, queue, events) |
| `GET /api/enrollments/{id}/runs?before=RUN&limit=N` | runs, newest first (`Server.Runs`: `RunInfo`) |
| `GET /api/enrollments/{id}/runs/{run}` | the run record: lanes, `After` graph, timings (`Server.Run`) |
| `GET /api/enrollments/{id}/runs/{run}/jobs/{lane}` | the lane's Doc: stages, steps, results (`RunReport.Jobs`) |
| `GET /api/enrollments/{id}/runs/{run}/jobs/{lane}/log` | raw log, `Range: bytes=` honored |
| `GET /api/events?enrollment=ID` | SSE: `snapshot`, `run`, `log` (run, lane, new offset) |

- Events: one hub-wide broadcaster fed by `report`, run puts and log
  appends; per-client buffered channels, dropped (client reconnects with
  `Last-Event-ID`) rather than blocking a report. A heartbeat comment
  every 15s for proxies.
- `GET /api/workers` goes; `GET /api/enrollments` replaces it (schema
  `swim.hub/v2`), and `Admin.Workers` reads that.

**Done when:** each route has a test with and without a session under
`--private`; an SSE client sees a `log` event within one report tick of a
worker's append; a stalled client doesn't delay `report`.
