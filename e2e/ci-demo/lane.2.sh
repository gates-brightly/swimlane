#!/usr/bin/env bash
# swim: syntax 2
# Round: ci demo: a dry-run change and a drift note
# Job:     c1d3e000-0002-4000-8000-000000000002
# After:   1
# Owner:   swim e2e
# Guards:  CI_DEMO_ALLOW_WRITE write demo.txt - 2026-10-09, shows a dry-run notice
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2
stage snapshot
snapshot "current files" ls -la
stage check
gate "nothing to write yet" test ! -e demo.txt
stage change
if guard CI_DEMO_ALLOW_WRITE "write demo.txt - 2026-10-09, shows a dry-run notice"; then
  run "write demo.txt" bash -c 'echo demo > demo.txt'
fi
stage verify
drift "demo: a drift note shows as a warning annotation"
summary
