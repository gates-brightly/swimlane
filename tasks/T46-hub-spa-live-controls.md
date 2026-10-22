# T46. The hub app: live, controls, approvals inbox (P1, hub, gap)

Part of [swim.plan.md](../swim.plan.md) Phase 3. Needs T45.

**Today:** commands go through forms on `/w/{id}` (`pages.go`), posted to
`POST /api/workers/{id}/commands` with a CSRF field; guards waiting on a
headless worker are listed only on that worker's page.

**Do:**
- One `EventSource` per tab (`GET /api/events`, `internal/hub/events.go`): runs, run and job views update in
  place; a running job's log appends and follows (pauses when scrolled
  up, with a "jump to end"); the live dot shows reconnecting.
- Controls in the app, shown only where `Can(kind)` allows today: pause,
  unpause, interrupt, run now (pinned to the script hash), trash, drain,
  answer (sign, or approve/decline where `headless.approvals: hub`). Same
  endpoint and CSRF token (sent as a header), confirmation on the
  destructive ones, the command's outcome shown as it changes.
- Approvals: every guard waiting across all enrollments, newest first,
  with its lane, step, evidence hash and how long it has waited; the top
  bar shows the count.
- Remove `/classic` and the old templates.

**Done when:** a lane's new log line shows in an open job view within one
report tick (e2e with a headless worker); every command the old page
sent works from the app and is refused there for a viewer; a guard
waiting on one worker shows in the inbox and clears once answered.
