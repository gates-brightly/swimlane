# Interactive lane view for `swim all` / `swim run`

Status: proposed

## Summary

While `swim all` (or `swim run`) is running in a terminal, the operator can use
the keyboard to focus one lane and read only its log, scroll back through it,
then press ESC to return to the combined view of all lanes.

```
 swim · swimlane · main@64a0e07 · 0:42                    ← → lane  ↑ ↓ scroll  esc all
 swim 1  PASS     DAG root: fetch pages, aggregate               0:01
 swim 2  PASS     DAG child A: combined.html to Markdown         0:03
▶swim 3  running  DAG child B: per-page link and word stats      0:08  stats.json
 swim 4  waiting  DAG join A+B: report.md                        waiting on swim 3
 … 95 more (88 pass, 6 running, 1 waiting)
──────────────────────────────── swim 3 · following · .swim/logs/agent3.log ──
=== STEP 2026-10-09T13:40:28Z simulated work (10ms)
$ sleep 0.01
--- exit 0 (0.0s)
PASS  simulated work (10ms)
=== STEP 2026-10-09T13:40:28Z compute stats.json
$ e2etool stats
--- output
cern     words=  291 int= 14 ext=  0  The World Wide Web project
```

## Motivation

- **Combined output is hard to follow.** With more than a few lanes, the output
  below the panel is mixed line by line. Following one lane means reading past
  every `[N]` prefix that isn't it.
- **Detail lives elsewhere.** The step headers and PASS/FAIL lines are only in
  `.swim/logs/agentN.log`. Today the operator opens a second terminal and runs
  `tail -f` or `swim log N` there.
- **Output scrolls away.** Anything that has left the screen is gone from the
  live view, and with 99 lanes it goes in seconds.

## Behaviour

### Views

- **All view:** the default, and what you see today. The panel is pinned at the
  top, and every lane's output is mixed below it, prefixed `[N]`.
- **Lane view:** the panel stays, with the selected lane marked `▶`. Below it is
  only that lane's log for the current round, the same content as `swim log N`:
  step headers, output, `PASS`/`FAIL`/`SKIP` lines and the summary. It follows
  the log as it grows. A divider shows the lane, whether it is following or
  paused, and the log path.

### Keys

| key | all view | lane view |
|---|---|---|
| `→` / `l` / `Tab` | open lane view on the first lane in the panel, or the last lane selected | next lane |
| `←` / `h` / `Shift-Tab` | open lane view on the last lane | previous lane |
| `↑` / `k`, `↓` / `j` | scroll the combined output | scroll the lane's log one line |
| `PgUp` / `PgDn` | page | page |
| `Home` / `g`, `End` / `G` | top, bottom and follow | top, bottom and follow |
| digits, then `Enter` | jump to lane N (e.g. `4` `2` Enter) | jump to lane N |
| `f` | cycle the lane order: every lane / running / failed | same; ←/→ then move within that set |
| `Esc` | — | clear the lane filter and return to the all view, following |
| `Ctrl-C` | interrupt running lanes, as today | same |
| `?` | help overlay | help overlay |

- **Following:** at the bottom, a view follows new lines. Scrolling up pauses it.
  The divider then shows `paused · 37 new lines · End to follow`, and the view
  stays put while lanes keep writing.
- **Lane order:** left and right move through lanes in panel order (running
  first, then waiting, then finished), not by number. That way the lanes worth
  watching are adjacent. When the panel collapses lanes into "… 95 more", the
  selected lane always gets a row of its own.
- **Selection is kept:** if a lane finishes while selected, it stays selected
  and shows the end of its log.

### When the run ends

The view closes as it does today, and the normal summary prints into the
terminal's scrollback. If the operator is in a lane view or has scrolled up at
that moment, the screen stays open: the divider says
`run finished · q to close`. Then `q` (or `Esc` from the all view) closes it and
prints the summary. This means finishing never yanks away a log someone is
reading.

### When it's off

The existing views are used, unchanged, when any of these apply:

- **No terminal:** stdin or stdout isn't one.
- **Plain output:** `--plain`, `NO_COLOR` or `SWIM_COLOR=0` (plain mode today).
- **CI:** it's a CI run (`CI=true`, `swim ci`).
- **Terminal too small:** it has fewer rows than the panel plus 5, the current
  live-mode threshold.
- **Opted out:** `--no-tui` or `SWIM_TUI=0`.
- **A lane owns stdin:** a single-lane run currently passes stdin to the lane
  so `confirm` can read an answer. See open question 1.

## Design

- **Display mode:** a third mode, `interactive`, in `internal/display`,
  alongside `live` and `plain`. Today, live mode pins the panel with an ANSI
  scroll region and lets the terminal scroll the rest. Interactive mode needs a
  viewport it can move, so it:
  - draws the whole screen on the alternate screen buffer (`\e[?1049h`)
  - redraws at most every 50ms or so, coalescing changes
  - restores the main screen on exit, then prints the summary there
- **Buffers:**
  - The all view keeps a ring buffer of the combined lines `Display.Line(n, text)`
    already receives, capped at about 20k lines.
  - The lane view tails `.swim/logs/agentN.log` from the current round's
    `=== ROUND START` offset, using the same parsing as `swim log`. That gives
    it headers and result lines, which never reach stdout. It reads only what's
    needed for the screen plus a scroll margin, so a long log costs little.
- **Input** (`internal/display/keys.go`):
  - A goroutine puts stdin in raw mode (`term.MakeRaw`) and parses key
    sequences into events.
  - A lone `Esc` is told apart from the start of an arrow-key sequence
    (`ESC [ A`) with a short timeout, about 30ms.
  - Raw mode turns off the terminal's signal handling, so the parser turns
    byte `0x03` into the same interrupt the launcher's SIGINT handler runs
    today.
- **Restoring the terminal:** this is the risky part. Raw mode and the
  alternate screen must be undone on every exit path:
  - a normal finish
  - Ctrl-C
  - SIGTERM or SIGHUP
  - a panic in the display goroutine (recover, restore, re-panic)

  `Display.Restore` already handles the scroll region, so this extends it.
- **Launcher:** `launcher.Options.Stdin` goes to the display, not to a lane,
  whenever interactive mode is on. That's always true when more than one lane
  runs, since those lanes get no stdin today. Nothing else in the launcher
  changes: lanes, logs and status.yml are unaffected by the view.
- **Rendering:** `RenderPanel` gains a selected-lane marker and the rule that
  the selected lane is always visible. `WrapANSI` handles long lines in the all
  view. The lane view is plain text, because logs are ANSI-stripped.

## Open questions

1. **A single lane that needs stdin** (`confirm`). Options:
   - (a) no TUI for single-lane runs
   - (b) TUI with an "input" key that hands the keyboard to the lane until
     Enter
   - (c) detect a `confirm` prompt (the lane is blocked reading) and switch to
     plain mode for it

   (a) is simplest and matches today's rule that interactive prompts need a
   single lane.
2. **What should the lane view show?** Its log (recommended: complete, with
   headers and PASS/FAIL), or only the lane's stdout as in the all view? Could a
   key switch between them?
3. **Should the all view keep full scrollback in memory**, or only the last N
   lines, with older lines on disk only (in the logs)?
4. **Search:** `/` to search within the current view. Small to add once there's
   a viewport; maybe in a follow-up.
5. **Stopping one lane:** a key to send SIGINT to just the selected lane. That's
   useful, but it's a new capability (today interrupts are all or nothing), and
   it needs a confirm step and log semantics.

## Testing

- **Unit:**
  - the key parser: arrow, PgUp/PgDn and Home/End sequences from
    xterm/iTerm/Terminal.app/tmux; a lone Esc versus Esc-sequences split across
    reads; `0x03`
  - the viewport model: scrolling, following, the new-lines counter, the
    selection surviving lane-order changes and the panel collapsing
  - rendering: golden files in `internal/display/testdata`, as `render_test.go`
    does today
- **PTY e2e (`internal/e2e`):** run `swim all` under a pseudo-terminal, e.g.
  `github.com/creack/pty` (a new test-only dependency) or `script(1)`. Send
  keys and assert screen contents:
  - `→` shows only lane 2's log
  - `Esc` returns to the all view
  - `↑` pauses following
  - `Ctrl-C` interrupts and the summary prints

  Afterwards, assert the terminal is restored (`stty -a` matches the state
  before) after a normal finish, Ctrl-C, SIGTERM and a forced panic.
- **Scenarios:** run `e2e/scenarios/dag99` under the PTY harness at 99 lanes to
  check redraw cost and that the panel collapses around the selected lane. This
  is the stress case for redraw rate.
- **By hand:** macOS Terminal, iTerm2, tmux, VS Code's terminal, Linux xterm,
  and resizing during a run.

## Out of scope

- Mouse support.
- Editing or rerunning lanes from the TUI. It's a view, not a controller,
  except for the interrupt question above.
- A TUI for `swim status` or `swim log`. Those could reuse the viewport later.
