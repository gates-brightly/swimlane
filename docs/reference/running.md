# Running

`swim run N ...` (or `swim N`) runs lanes; `swim all` runs every ready
round that hasn't passed (`--rerun`: those too). Lanes run in parallel,
dependencies first.

## Dependencies

`# After: 2 3` (lanes, tasks or round ids) waits for those to pass in this
run, and skips the round if one fails or is skipped. It is the only way to
declare a dependency. A dependency outside the run must already be
satisfied, or the lane is skipped:

- a lane: its ready round passed (an archived or empty lane never blocks);
- a round id: it's that lane's last round, and passed.

Cycles, unresolvable references, and an `After:` lane with no script, log
or past run stop the run before anything starts.

`swim status` shows, under each ready lane, what it waits on and where
that stands, how each guard would be decided now, and its timeout, locks
and includes:

```
 lane 1  ready                  drop the old table
          guard DROP_OK  drop table old_orders
                 not approved; swim run will ask (a dry run if no one answers)
          timeout 30m · locks orders-db · includes lib/tables.sh
 lane 2  ready                  verify after the drop
          after lane 1 (ready: a dry run or failure there skips this)
```

Guards ask as the round reaches them; [guards](guards.md#how-a-guard-is-decided)
says how each is decided (`--no-prompt` answers every question no).

## Parallelism

`--parallel N` (config `max_parallel`; default unlimited) caps lanes
running at once; ready lanes queue for a slot, longest chain first.

## Locks

`# Locks: orders-db, tf/orders`: a lane takes all its locks at once, after
its dependencies pass. Lanes sharing one never overlap, in either order,
within a run and across concurrent runs on the machine (`.swim/locks/`,
released when the lane's process ends). A failure doesn't spread through a
lock. Not across machines, and not for a lane run directly with bash. Use
`After:` for order, `Locks:` when order doesn't matter but overlap does.

## The live view

A panel at the top of the terminal shows each lane's state; output scrolls
under it, prefixed `[N]`. With more than one lane:

| | |
|---|---|
| `→` `←` | Open a lane's log; `Esc` returns. |
| `↑` `↓` `PgUp` `PgDn` `Home` `End` | Scroll. |
| a number, `Enter` | Jump to that lane. |
| `f` / `?` | Filter to running or failed lanes / help. |
| `Enter`, `y`, `n` | Open the lane a banner says is waiting, and answer it. |

`--plain` (or no terminal): no live view, plain prefixed lines.
`SWIM_TUI=0`: the panel without the keys. `--yaml`: [events](logs.md#yaml).

`notify: true` in config adds a desktop notification when a lane waits on
you or a run ends, and a progress bar on the terminal tab (OSC 777, OSC
9;4, as Ghostty shows them). Each title starts with where it came from:
the repo (origin's name, else the checkout's directory), and the branch
when it isn't the default: `swimlane · lane 2 needs you`,
`swimlane@fix-auth · run failed`, `swimlane · run needs approval` (a guard dry-ran), `swimlane · batch #41 passed`.

`notify_url` sends them to an [ntfy](https://ntfy.sh) topic, terminal
or not, to reach your phone:

```yaml
defaults:
  notify_url: https://ntfy.example.com/swim
```

Each is an ntfy message: its title, `high` priority for a question or a
failure, and tags for how it ended and the repo, sent with `curl`.

`notify_command` runs a command of yours instead, for anything else (a
webhook, a chat): `sh -c` in the repo root, with the notification in its
environment, and `notify_url` as `$SWIM_NOTIFY_URL`:

| Variable | |
|---|---|
| `SWIM_EVENT` | `ask` (a lane waits on you), `end` (a run or batch ended) or `test` (`t` on `swim config`'s screen). |
| `SWIM_RESULT` | For `end`: `passed`, `failed`, `interrupted`, `skipped` (a guard dry-ran: it waits on an approval) or `error`. |
| `SWIM_TITLE`, `SWIM_BODY` | What the terminal notification says, one line each. |
| `SWIM_LANES` | The lanes, `1,2,3`. |
| `SWIM_BATCH` | `swim watch`'s batch number (empty for `swim run`). |
| `SWIM_REPO`, `SWIM_BRANCH` | Where it came from: the repo's name (as the title says it) and the branch checked out (empty when detached). |
| `SWIM_ROOT` | The repo. |
| `SWIM_PRIORITY`, `SWIM_TAGS` | ntfy's: `high` for a question or a failure (else `default`), and an emoji's name for how it ended, then the repo (`x,swimlane`). |
| `SWIM_NOTIFY_URL` | `notify_url`. |
| `SWIM_APPROVE_URL`, `SWIM_REJECT_URL` | For `ask` under `swim watch` with `phone` set: the question's links (see below). Empty otherwise. |
| `SWIM_EXPIRES` | When those links stop working (RFC 3339). |
| `SWIM_ACTIONS` | The links as ntfy's `Actions` header: an Approve and a Decline button. |

Quote the variables: the body carries a lane's own text. Each send is
killed after 15s; swim waits for it before exiting.

Whether it's working: `swim config` shows the last send under the two
keys (`last sent 3m 0s ago: swimlane · run passed`, or why it failed;
kept in `~/.local/state/swim/notify.json`), and `t` on its screen sends a
test. `swim doctor` warns while sends fail (`notify-failing`, `notify`),
and each failure is a `warn` line in `.swim.log`.

### Answering from your phone

With [Tailscale](https://tailscale.com) on this machine and your phone,
`phone` lets one tap on a notification answer a question `swim watch`
asks:

```yaml
defaults:
  notify_url: https://ntfy.sh/TOPIC
  phone: zachs-iphone   # its tailnet name (tailscale status) or 100.x address
  phone_expiry: 15m     # the default
```

Each question's notification then carries two links: ntfy shows them as
**Approve** and **Decline** buttons, and `notify_command` gets them as
`$SWIM_APPROVE_URL` and `$SWIM_REJECT_URL`. A tap answers the way `y` or
`n` does at the screen, and the lane's log and the chain say so:
`approved by zach on phone zachs-iphone`, method `phone`.

What keeps a tap yours:

- `swim watch` serves the links on this machine's tailnet address alone,
  on a port it picks each time it starts, and answers a request only when
  it comes from the phone's address. A request from anywhere else, this
  machine included (where lanes run), is refused before its token is read,
  and is a `warn` line in `.swim.log`. A link that leaks (ntfy's cache, a
  screenshot) answers nothing.
- Each link has its own random 128-bit token, kept in `swim watch`'s
  memory and never written to `.swim/` or `.swim.log`. It answers one
  question, once: the first answer, on the phone or at the screen, ends
  both of the question's links.
- Each link stops working `phone_expiry` after the question was asked.
  The question then waits for the screen.

Tailscale must be connected on the phone when you tap: if it isn't, the
tap does nothing and the question still waits. Phone answers need the
watch screen and `watch_batches` of 2 or more (the default is 4).
`swim run`'s questions are still answered only at its terminal.

## Interrupts

Lanes run in their own process groups, so Ctrl-C reaches only swim:

| | |
|---|---|
| 1st Ctrl-C | Graceful: each running lane finishes its step, starts no other, and ends interrupted (exit 130); lanes not started are skipped. |
| 2nd | SIGINT to every running lane's group, SIGKILL after `interrupt_grace` (5s). |
| 3rd | SIGKILL now. |

SIGTERM (a CI cancel) stops gracefully, then forces after `term_grace`
(10s); SIGHUP forces at once. `interrupt: immediate` makes the first press
force. `swim interrupt N` stops one lane from another terminal. A lone
lane given the terminal gets Ctrl-C directly. A lane run directly with bash
has no launcher: Ctrl-C interrupts it at once.

A lane whose process died: [recovery](recovery.md#a-lane-stuck-running).
