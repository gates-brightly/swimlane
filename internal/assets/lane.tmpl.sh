#!/usr/bin/env swim
# swim: syntax {{.Syntax}}
# Round:    {{.Goal}}
# Round-ID: {{.Job}}
# Owner:    {{.Owner}}
{{if .Toolchain}}
{{.Toolchain}} || _swim_stop "toolchain failed to load"
{{end}}
stage snapshot
snapshot "current state" echo "replace with a read-only describe/list command"

stage check
gate "target is ours (tagged)" true

stage change
# A destructive step goes behind a guard, declared in the header with
# what it does and, on the line under it, why ({{.Date}}):
#   # Guards: EXAMPLE_ALLOW_DELETE  delete X (the 3 rows check counted)
#   #   why: replaced by Y; the snapshot keeps them ({{.Date}})
# It's a dry run until approved.
# if guard EXAMPLE_ALLOW_DELETE; then
#   step "delete X" echo "would delete X"
# fi

stage verify
step "verify" true
