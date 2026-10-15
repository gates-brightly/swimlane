#!/usr/bin/env bash
# swim: syntax 2
# Round:  Release 2.4.0: end-to-end smoke test
# After:  2 3 4
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 5

stage verify
run "checkout flow" sleep 1
run "every service on 2.4.0" bash -c 'for s in api web worker; do [ "$(fakecloud version $s)" = 2.4.0 ] || { echo "$s is $(fakecloud version $s)"; exit 1; }; done'
summary
