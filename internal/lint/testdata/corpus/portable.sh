#!/usr/bin/env bash
# swim: syntax 4
# Round: the portable forms of GNU-only tools
# Job:   corpus-portable
# Timeout: 30m
#
# What gnu-only warns about, written so a Mac's BSD tools run it too, and
# named where bash doesn't run it. Lint reports nothing for this script.
_swim_lib=$("${SWIM_BIN:-swim}" _lib) || { echo "swim not found" >&2; exit 1; }
eval "$_swim_lib"
lane_init 1
stage snapshot
snapshot "count" grep -cE '^[0-9]+$' notes.txt
snapshot "size" wc -c notes.txt
snapshot "logs" find . -name '*.log' -print
snapshot "today" date -u +%F
stage check
# comment: sed -i 's/a/b/' f; grep -P x; date -d now; stat -c %s f; timeout 5 x
echo "sed -i s/a/b/ f; grep -P x; date -d now; stat -c %s f; find . -printf x"
echo 'timeout 30 sleep 1'
stage change
run "edit" sed -i.bak 's/a/b/' notes.txt
run "drop backup" rm -f notes.txt.bak
run --timeout 30s "slow" sleep 1
stage verify
gate "edited" grep -q b notes.txt
summary
