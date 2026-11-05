#!/usr/bin/env swim
# swim: syntax {{.Syntax}}
# Round:   {{.Goal}}
# Job:     {{.Job}}
# Owner:   {{.Owner}}
{{if .Toolchain}}
{{.Toolchain}} || _swim_stop "toolchain failed to load"
{{end}}
stage snapshot
snapshot "current state" echo "replace with a read-only describe/list command"

stage check
gate "target is ours (tagged)" true

stage change
# A destructive step goes behind a guard: list it in the header
# (# Guards: FLAG  what it approves); it's a dry run until approved.
# if guard EXAMPLE_ALLOW_DELETE "delete X ({{.Date}}: why it is safe)"; then
#   run "delete X" echo "would delete X"
# fi

stage verify
run "verify" true
