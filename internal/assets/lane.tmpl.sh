#!/usr/bin/env bash
# swim: syntax {{.Syntax}}
# Round:   {{.Goal}}
# Job:     {{.Job}}
# After:
# Owner:   {{.Owner}}
# Created: {{.Date}}
# Guards:
# Timeout:
#
# The header: After lists lanes, task names or job ids that must pass
# first; Guards takes one line per flag, "FLAG  what it approves" (a guard
# not listed dry-runs); add an Includes line for files the lane reads, as
# it runs in a staged copy of only those. Don't use `set -e`: failed checks
# keep going, and gate stops the round. swim guide has the rest.
# Run it with swim run {{.Lane}}

_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found on PATH" >&2; exit 1; }
eval "$_swim_lib"
lane_init {{.Lane}}
{{if .Toolchain}}{{.Toolchain}} || stop "toolchain failed to load"
{{end}}
stage snapshot
snapshot "current state" echo "replace with a read-only describe/list command"

stage check
gate "target is ours (tagged)" true

stage change
# if guard EXAMPLE_ALLOW_DELETE "delete X ({{.Date}}: why it is safe)"; then
#   run "delete X" echo "would delete X"
# fi

stage verify
run "verify" true

summary
