# swim on the web: plan

Goal: make swim web-first, served from spiff (the NixOS home server) at
https://swim.stern-tone.ts.net, without weakening the trust model
([docs/trust-model.md](docs/trust-model.md)).

The web side already exists: `swim hub` (`internal/hub`) is a stdlib
`net/http` server with server-rendered pages, a JSON API, users with roles,
session cookies with CSRF, and long-polled commands. Workers (`swim watch`,
one per repo) push snapshots to it and pull commands from it; the hub never
reaches into a repo, runs anything or holds a key that makes an approval
valid. This plan grows the hub instead of building a second web app.

## Where it stands

- **Routes**: `hub.go:8-35`. Pages: `/`, `/w/{id}`, `/w/{id}/logs/{run}/{lane}`,
  `/w/{id}/answer/{req}`, `/w/{id}/trash/{lane}`. API: enroll, report,
  workers, grants, checkpoints, users, login/logout, commands, logs.
- **Auth**: bearer tokens (enroll, per-worker, admin, per-user). A browser
  logs in with a user token and gets an HttpOnly, SameSite=Strict cookie
  that lasts 12 hours; sessions live in memory only (`users.go`). Viewing
  the pages and `GET /api/workers` needs no login (`hub.go:43`).
- **Approvals from the hub**: a pasted SSH signature checked against
  `lookup.yml`, or a button press under `headless.approvals: hub` for guards
  no `require:` rule names, recorded as `hub-button` (`launcher/authority.go:252-298`).
  Which commands the hub may send is set by the committed `swim.yml`
  `headless:` block (`config/headless.go`).
- **Serving**: `127.0.0.1:7700` by default; plain HTTP is allowed only on
  loopback (`cli/hubtls.go:17`), which fits a TLS proxy like `tailscale serve`.

Gaps:

- No live updates: pages reload every 2-3s (`pages.go:124,146,198`); a lane's
  live output is the last 15 lines of its snapshot.
- The hub sees only the latest snapshot: no history, plan, timeline or
  verify results.
- Behind a TLS proxy: cookies aren't `Secure` (`cli/hub.go:144`), audit logs
  record 127.0.0.1 (`users.go:263,280`), and the enroll hint prints the
  internal address (`cli/hub.go:111-122`).
- Pages are Go string constants (`pages.go`), with no embedded assets.
- No Nix packaging.

## Boundaries (keep these through every phase)

- The hub holds no runner, approver or secrets key. Nothing it stores can
  make an approval valid on its own.
- The hub never writes a repo's `.swim/` state. Changes go through worker
  commands, so `status.Transition` and the chain stay authoritative.
- Never render what lanes produce without masking: `.swim/out/`,
  `evidence/`, `snapshots/` and kept scripts aren't redacted. Escape and
  sanitize all lane text (`hub.Sanitize`, `ui.Safe`).
- Approving from a browser stays what `authority.go` allows today: a pasted
  SSH signature, or the hub button where policy permits it. Anything
  stronger (passkeys, Tailscale identity as an approver) is a trust-model
  change first and a UI change second.

## Phase 0: the current hub at swim.stern-tone.ts.net

No changes to swim's behavior.

- [ ] `flake.nix` here: `packages.<system>.swim` (built with `buildGo127Module`,
      `cmd/swim` only, the Makefile's version ldflags) and a dev shell.
- [ ] In the nix repo: swimlane as a flake input; `hosts/spiff/swim.nix` runs
      `swim hub --addr 127.0.0.1:3024 --state /var/lib/swim-hub --no-tui`,
      with `SWIM_HUB_TOKEN` from a generated secret. The hub makes and keeps
      its own admin token in the state directory.
- [ ] `homelab.apps.swim`, so it's the Tailscale Service `svc:swim` at
      https://swim.stern-tone.ts.net; a Homepage tile; `svc:swim` in
      `tailnet/policy.hujson`.
- [ ] Enroll the Mac's repos (`swim hub enroll https://swim.stern-tone.ts.net
      --token ...`) and keep `swim watch` running in them.

Done when `just ts-test` shows `swim 200` and the dashboard lists a worker.

## Phase 1: at home behind `tailscale serve`

- [ ] `--public-url URL`: the address clients use. Marks cookies `Secure`
      and prints it in the enroll hint.
- [ ] Trust `X-Forwarded-For` from loopback only, so audit logs name the
      real client.
- [ ] `--private`: viewing pages and `GET /api/workers` needs a login.
- [ ] Sign in with Tailscale: with the hub reachable only through
      `tailscale serve`, map its `Tailscale-User-Login` header to a hub user
      (viewer or operator by mapping, never an approver). Off unless
      `--trust-tailscale-identity`; refused on a non-loopback listener.
- [ ] Sessions survive a restart (kept in the state file, hashed).

## Phase 2: a web app, not a polled page

- [ ] Server-sent events (`GET /api/events`, per worker): fan out each
      report as it arrives; replace the meta refresh; a live log tail.
- [ ] Workers report more: recent run records, `plan`, `verify` status, and
      the timeline (`internal/timeline` already renders SVG).
- [ ] `--json` next to `--yaml` for status, log, plan, timeline and verify,
      the same schemas, so the CLI and the hub share one format.
- [ ] Pages move to embedded templates and static files (`embed.FS`),
      still server-rendered with no build step.

## Phase 3: run work from the browser

- [ ] A headless worker on spiff (`swim watch --headless --install systemd`)
      for repos checked out there. Their runs use spiff's user and
      credentials, so `run` goes in `headless.allow` repo by repo, on purpose.
- [ ] Design note for stronger browser approvals before any code.

## Before and alongside

- T29 (e2e red on main) first or in parallel, so hub changes get a reliable
  CI signal.
- New hub code goes in `internal/hub`; move hub wiring out of
  `internal/cli/hub.go` as it's touched (`internal/cli` is ~21k lines).
- Each phase's items become tasks in `tasks/` as they start; this file is
  deleted when the last phase ships.
