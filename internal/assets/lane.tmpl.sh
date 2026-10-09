#!/usr/bin/env bash
# swim: syntax 2
# Round:   {{.Goal}}
# Job:     {{.Job}}
# After:
# Owner:   {{.Owner}}
# Created: {{.Date}}
# Guards:
# Timeout:
#
# Header keys (empty means the default):
#   After:   lanes (or job ids) that must pass first, e.g. "1" or "2 3"   (default: none)
#   Guards:  one line per guard flag: "FLAG  what it approves (date, reason)"
#            (flags used in the body are found anyway; list them to explain them)
#   Timeout: limit for the whole round, e.g. 30m or 1h30m   (default: none)
#
# Goal:
#   {{.Goal}}
#
# Stages (every job has these, in this order; one a job doesn't need is left empty):
#   snapshot  read-only capture of current state (saved under .swim/snapshots/)
#   check     checks; gates stop the round before any change
#   change    the change itself; destructive steps behind guard flags
#   verify    prove it worked (e.g. re-plan is a no-op)
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
stage snapshot
snapshot "current state" echo "replace with a read-only describe/list command"

stage check
run "precheck" true
gate "target is ours (tagged)" true

stage change
# One guard flag per destructive action; list it under Guards: above.
# if guard EXAMPLE_ALLOW_DELETE "delete X ({{.Date}}: why it is safe)"; then
#   run "delete X" echo "would delete X"
# fi

stage verify
run "verify" true

summary
