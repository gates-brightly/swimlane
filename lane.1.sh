#!/usr/bin/env bash
# swim: syntax 2
# Round:   CI: fmt (gofmt -s)
# Job:     5c1a0011-11a7-4c1e-8d00-000000000011
# Always:  yes
# Owner:   swim CI
# Created: 2026-10-14
# Timeout: 5m
#
# Goal: every Go file is gofmt -s clean. An Always lane: swim all and swim ci
# run it fresh, before every other lane. It only checks; it never rewrites
# files (run make fmt for that).
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage check
gate "gofmt -s" bash -c 'out=$(gofmt -s -l .); [ -z "$out" ] || { echo "needs gofmt -s (make fmt):"; echo "$out"; exit 1; }'

summary
