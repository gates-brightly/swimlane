#!/usr/bin/env bash
# Run swim ci on the demo rounds in this directory, in a scratch git repo,
# so the CI provider's rendering (groups, annotations, job summary) can be
# checked by eye. Used by .github/workflows/test.yml; works locally too
# (generic output). Reports and logs land in $SWIM_CI_DEMO_OUT (default
# ./ci-demo-out). Bash 3.2 compatible.
set -u
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
out=${SWIM_CI_DEMO_OUT:-$PWD/ci-demo-out}
mkdir -p "$out"
work=$(mktemp -d "${TMPDIR:-/tmp}/swim-ci-demo.XXXXXX") || exit 1
trap 'rm -rf "$work"' EXIT
(cd "$root" && go build -buildvcs=false -o "$work/bin/swim" ./cmd/swim) || exit 1
export PATH="$work/bin:$PATH" SWIM_BIN="$work/bin/swim" XDG_CONFIG_HOME="$work/xdg"
repo=$work/repo
mkdir -p "$repo" && cd "$repo" || exit 1
git init -q .
cp "$here"/swim.yml "$here"/lane.*.sh .
chmod +x lane.*.sh
# A fresh runner: no ~/.config/swim, the rounds and swim.yml committed.
GIT_AUTHOR_NAME=ci GIT_AUTHOR_EMAIL=ci@example.com GIT_COMMITTER_NAME=ci GIT_COMMITTER_EMAIL=ci@example.com \
  git add -f swim.yml lane.*.sh >/dev/null
GIT_AUTHOR_NAME=ci GIT_AUTHOR_EMAIL=ci@example.com GIT_COMMITTER_NAME=ci GIT_COMMITTER_EMAIL=ci@example.com \
  git commit -q -m "ci demo rounds"
swim ci --junit "$out/swim.xml"
code=$?
cp -R .swim/logs "$out/logs" 2>/dev/null
cp .swim.log "$out/" 2>/dev/null
exit $code
