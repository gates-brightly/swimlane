#!/usr/bin/env bash
# swim: syntax 4
# Round: the quiet corpus's words, run for real
# Job:   corpus-loud
# Timeout: 30m
#
# What testdata/corpus/quoted.sh only names, run as commands (each line's
# expected finding after it): lint reports every one.
_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found" >&2; exit 1; }
eval "$_swim_lib"
lane_init 1
stage snapshot
snapshot "list" ls
stage check
run "plan" terraform apply -auto-approve # check-unsafe
run "wipe" bash -c 'rm -rf /tmp/x' # destructive
T="$(swim secret get TOKEN)" # secret-ref, secret-guard
curl -H "token: $(swim secret get TOKEN)" u # secret-argv
input 2 name # input-dep
run "peek" bash -c 'cat "$(swim input 2 name)"' # input-dep
mod clone x # module
AWS_PROFILE=prod aws s3 ls # ambient-credential
stage change
stage verify
summary
