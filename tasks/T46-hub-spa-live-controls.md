# T46. The hub app: live, controls, approvals inbox (P1, hub, gap)

Part of [swim.plan.md](../swim.plan.md) Phase 3.

**Today:** the app (`internal/hub/web/`, T45) shows enrollments, runs,
jobs, lanes, the queue and every waiting guard, read-only, and updates
lanes, the queue and the runs list from `GET /api/events` (`api.js`).
Commands still go through forms on the classic `/w/{id}` (`pages.go`),
posted to `POST /api/workers/{id}/commands` with a CSRF field; the app's
Approvals page links each guard to the classic answer page. `GET /api/me`
gives the app the session's CSRF token.

**Do:**
- A running job's log appends and follows (pauses when scrolled up, with
  a "jump to end"), from T43's stream and its `log` events; a run or job
  page open while its run arrives updates in place.
- Controls in the app, shown only where `Can(kind)` allows today: pause,
  unpause, interrupt, run now (pinned to the script hash), trash, drain,
  answer (sign, or approve/decline where `headless.approvals: hub`). Same
  endpoint and CSRF token (sent as a header), confirmation on the
  destructive ones, the command's outcome shown as it changes.
- Approvals: answer in the app (sign, or the button where allowed), with
  the guard's evidence and how long it has waited.
- Remove `/classic` and the old templates.

**Done when:** a lane's new log line shows in an open job view within one
report tick (e2e with a headless worker); every command the old page
sent works from the app and is refused there for a viewer; a guard
waiting on one worker shows in the inbox and clears once answered.
