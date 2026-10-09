# Chime when a run finishes

Status: proposed

## Summary

When `swim run` / `swim all` finishes, swim rings the terminal bell, and can
also play a sound or show a desktop notification, so an operator who has
switched to another window knows to come back. It's off by default and set
per operator:

```sh
swim config chime true          # bell on every finish
swim config chime failure       # only when something failed or was interrupted
swim config chime false
```

```yaml
defaults:                       # ~/.config/swim/config.yml (the operator's own file)
  chime: true                   # true | false | failure
  chime_style: bell             # bell | sound | notify
  chime_min_s: 10               # don't chime for runs shorter than this
```

## Motivation

- **Runs are long and the operator looks away:** rounds run for minutes to
  hours (Terraform plans, migrations, VPN-only checks). The operator starts
  `swim all`, switches to Slack or the planner's session, and finds out it
  finished, or that a gate stopped it 20 minutes ago, only by checking back.
- **A run that stops early matters most:** a gate that stopped the round is
  often waiting on a human decision.

## Behaviour

- **When:** after the summary prints, at the end of `swim run`/`swim all`
  (including a Ctrl-C interrupt), the chime fires once if:
  - `chime` is `true`, or it's `failure` and the run didn't fully pass
  - the run took at least `chime_min_s` seconds (default 10), so quick runs
    stay quiet
  - stdout is a terminal, unless the style is `notify`, and it isn't a CI run
    (`CI=true`, `swim ci`)
- **Styles:**

  | `chime_style` | what happens | where |
  |---|---|---|
  | `bell` | writes `\a`; most terminals flash or beep, and tmux/iTerm mark the tab | any terminal |
  | `sound` | `bell`, plus a short system sound: macOS `afplay /System/Library/Sounds/Glass.aiff` (`Basso` on failure); Linux `canberra-gtk-play` or `paplay` if present | falls back to `bell` |
  | `notify` | `bell`, plus a desktop notification, "swim: 97 passed, 2 failed (0:42)": macOS `osascript -e 'display notification …'`; Linux `notify-send` if present | falls back to `bell` |

  On failure the bell rings twice, so success and failure sound different.
- **Override for one run:** `--chime` / `--no-chime`.
- **Commands that never chime:** `swim step`, `swim plan`, `swim status` and
  other short commands.

### `swim config <key> <value>`

Today `swim config` shows the effective config and takes `--lanes N`. This
adds a key/value form for operator preferences:

- `swim config chime true` writes `chime: true` under `defaults:` in the
  operator's config file, keeping comments, as `SetLanes` does for `lanes`.
- `swim config chime` prints the current value.
- `--repo` writes the setting to this repo's section instead of `defaults:`.
- Unknown keys or bad values are rejected with the allowed values.

`lanes` would also work as `swim config lanes 99`, with `--lanes` kept as an
alias. That gives one consistent way to set things.

## Design

- **Config:** `Chime string` (normalised to `on|off|failure`),
  `ChimeStyle string` and `ChimeMinS int` in `internal/config`. The YAML reads
  booleans or the `failure` string. Writing reuses the comment-preserving
  editor behind `SetLanes`, generalised to `SetKey(path, root, key, value)`.
- **Firing:** a `chime` step at the end of the `run` command in
  `internal/cli`, after `printSummary`, given the run's outcome and duration.
  - Sound and notify run a command with a 2s timeout, detached so they can't
    delay swim's exit.
  - Any error is ignored, because a chime must never change the exit code.
- **Display:** in the live view, restore the scroll region before writing
  `\a` so the bell doesn't land in the panel. The TUI ([tui.md](tui.md)) does
  the same when it leaves the alternate screen.

## Open questions

1. **Chime when a gate stops a lane** mid-run, not only at the end? That's
   useful when a stopped lane blocks others for a long time. Recommend a
   later `chime: stop` mode.
2. **Custom sound file or command** (`chime_command: say "swim done"`)? It's
   flexible, but it runs an arbitrary command from config. Probably fine,
   since it's the operator's own file, but it should be noted.
3. **Should `swim ci` post to chat instead?** Out of scope: CI providers
   already notify.

## Testing

- **Unit:**
  - the decision table: chime on/off/failure × pass/fail/interrupt × duration
    above or below the minimum × terminal or not × CI
  - `swim config chime` set and get round-trips, comments are preserved, bad
    values are rejected
- **Go e2e:**
  - under a PTY with `chime: true` and a minimum of 0, the output ends with
    `\a`
  - with `failure`, a passing run has no `\a` and a failing one has `\a\a`
  - with stdout not a terminal, no `\a`
- **Sound and notify:** the external command is injected in tests and
  asserted, never actually played.
