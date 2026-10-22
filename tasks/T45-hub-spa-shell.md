# T45. The hub as a single-page app: shell, runs, run, job (P1, hub, gap)

Part of [swim.plan.md](../swim.plan.md) Phase 3. Needs T44.

**Today:** three server-rendered pages built from Go string constants
(`internal/hub/pages.go`), reloaded every 2-3s.

**Do:**
- `internal/hub/web/` embedded with `embed.FS`: `index.html`, ES modules,
  CSS, and Preact + htm vendored (pinned, with their licenses). No build
  step, no Node; `go build` stays the whole build. Served with
  `Content-Security-Policy: default-src 'self'` and history-API routing
  (any unknown non-API path gets `index.html`).
- Top bar: swim hub · enrollment switcher (namespace, then worker, with
  state dots; `Cmd/Ctrl-K` opens it) · Runs · Lanes · Queue · Approvals
  (count) · live/offline dot · user menu (log in / out).
- Routes:
  - `/e/{id}/runs`: status, run id, lanes, by, duration, when.
  - `/e/{id}/runs/{run}`: the lanes as a graph from `After` (inline SVG,
    the layout `internal/timeline` uses), and per lane its counts, guards
    (approved, `-y`, dry run), exit and script hash.
  - `/e/{id}/runs/{run}/jobs/{lane}`: jobs down the left; on the right
    the summary over the log, stages and steps folding (failed open),
    each with its command, exit and duration; line numbers, `#L120`
    links, search, raw download.
  - `/e/{id}/lanes`, `/e/{id}/queue`: what the worker page shows now.
- Today's pages move under `/classic` (links in the user menu) until T46
  covers their controls; then they go.
- Light and dark from the existing tokens (`pages.go` `:root`); usable at
  phone width.

**Done when:** every route renders from fixtures in a Go test that
serves the embedded files; the CSP header is on every response and the
app works under it (no inline script or style attributes); lane text
with HTML and escape sequences renders as text; `/classic` works as `/`
did.
