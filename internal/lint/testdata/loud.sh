#!/usr/bin/env bash
# swim: syntax 4
# Round: the quiet corpus's words, run for real
# Job:   corpus-loud
# Timeout: 30m
#
# What testdata/corpus/quoted.sh only names, run as commands (each line's
# expected finding after it): lint reports every one.
_swim_lib=$("${SWIM_BIN:-swim}" _lib) || { echo "swim not found" >&2; exit 1; }
eval "$_swim_lib"
lane_init 1
stage snapshot
snapshot "list" ls
stage check
step "plan" terraform apply -auto-approve # check-unsafe
step "wipe" bash -c 'rm -rf /tmp/x' # destructive
stage change
stage verify
summary
