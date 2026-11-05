# Running

`swim run N ...` (or `swim N`) runs lanes; `swim all` runs every pending
round that hasn't passed (`--rerun`: those too). Lanes run in parallel,
dependencies first.

## Dependencies

`# After: 2 3` (lanes, tasks or job ids) waits for those to pass in this
run, and skips the round if one fails or is skipped. It is the only way to
declare a dependency. A dependency outside the run must already be
satisfied, or the lane is skipped:

- a lane: its pending round passed (an archived or empty lane never blocks);
- a job: it's that lane's last run, and passed.

Cycles, unresolvable references, and an `After:` lane with no script, log
or past run stop the run before anything starts.

`swim status` shows, under each pending lane, what it waits on and where
that stands, how each guard would be decided now, and its timeout, locks
and includes:

```
 swim 1  pending                drop the old table
          guard DROP_OK  drop table old_orders
                 not approved; swim run will ask (a dry run if no one answers)
          timeout 30m · locks orders-db · includes lib/tables.sh
 swim 2  pending                verify after the drop
          after swim 1 (pending: a dry run or failure there skips this)
```

## Approving guards

Three ways, and only three: answer the prompt (`Y/N` on a terminal, `y`/`n`
in the live view), set the flag up front (`FLAG=1 swim run N`), or approve
a queued batch (`swim queue N --approve FLAG`, or `y` on `swim watch`'s
screen). `--no-prompt` answers every question no: each unset guard is a
dry run. In CI, with `--yaml` or with no terminal nothing asks either. See
[guards](guards.md#how-a-guard-is-decided).

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
`swimlane@fix-auth · run failed`, `swimlane · batch #41 passed`.

`notify_url` sends the same notifications (a lane waits on you, a run or
a `swim watch` batch ends) to an [ntfy](https://ntfy.sh) topic, whether
or not there's a terminal, to reach your phone:

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

Quote the variables: the body carries a lane's own text. Each send is
killed after 15s; swim waits for it before exiting.

Whether it's working: each send is recorded in
`~/.local/state/swim/notify.json` (`$XDG_STATE_HOME`). `swim config`
shows the last under the two keys (`last sent 3:00 ago: swimlane · run passed`,
or why it failed), and `t` on its screen sends a test; `swim doctor` warns
(`notify-failing`) while the last send failed, and (`notify`) when
`notify_url` doesn't answer or `notify_command`'s program isn't there, and each failure is also a
`warn` line in the repo's `.swim.log`. Both keys belong in `config.yml`,
never a repo's `swim.yml`: a committed file mustn't run a command no
guard sees, or send your notifications elsewhere.

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

`swim status` shows `running?` when a lane's process is gone; the next
`swim run`, `swim all` or `swim watch` records it as interrupted.
