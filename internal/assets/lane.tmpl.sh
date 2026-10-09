#!/usr/bin/env bash
# Round: {{.Goal}}
# Job:   {{.Job}}
# After:
#   ^ lanes (or job ids) that must pass before this round starts, e.g.
#     "# After: 1" or "# After: 2 3". Leave empty to start right away.
# Lane:  swim {{.Lane}}    Written: {{.Date}}
#
# Goal:
#   {{.Goal}}
#
# Steps:
#   1. Snapshot current state (read-only, saved under .swim/snapshots/)
#   2. Checks; gates stop the round before any destructive step
#   3. Change (destructive steps behind guard flags)
#   4. Verify (e.g. re-plan is a no-op)
#
# Guard flags this round honours (flag: action, date, reason):
#   (none)
#
# Run:   swim run {{.Lane}}    (or pinned to this job: swim run {{.Job}})
# $SWIM_JOB holds the job id while the round runs; tag resources with it.
# Never edit this file while it may be running: `swim status` first.
# Do not use `set -e`: failed checks keep going; only gates stop the round.

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init {{.Lane}}
{{if .Toolchain}}
# Pinned toolchain (non-interactive shells don't load version managers).
{{.Toolchain}} || stop "toolchain failed to load"
{{else}}
# Pinned toolchain: none configured (set `toolchain` in ~/.config/swim/config.yml).
{{end}}
# 1. Snapshot (read-only)
snapshot "current state" echo "replace with a read-only describe/list command"

# 2. Checks
run "precheck" true
gate "target is ours (tagged)" true

# 3. Change — one guard flag per destructive action
# if guard EXAMPLE_ALLOW_DELETE "delete X ({{.Date}}: why it is safe)"; then
#   run "delete X" echo "would delete X"
# fi

# 4. Verify
run "verify" true

summary
