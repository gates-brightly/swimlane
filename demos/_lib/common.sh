# Helpers for the demos. Each demo.sh sources this, calls demo_init, then
# walks through its scenes with say / show / pause.
#
# A demo runs in a scratch repo under ${TMPDIR:-/tmp}/swim-demos/<demo>,
# with its own HOME (SSH keys, swim config, secrets), so it never touches
# your ~/.ssh, ~/.config/swim or this checkout's .swim. The scratch repo is
# wiped at the start of each run and kept afterwards for poking around.
#
#   DEMO_AUTO=1   don't wait for Enter between scenes (a smoke test)
#   SWIM_BIN=...  use that swim instead of building one from this checkout
#
# Bash 3.2 compatible (macOS).

DEMO_LIB=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
DEMOS=$(dirname "$DEMO_LIB")
ROOT=$(dirname "$DEMOS")

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  _c_say=$'\033[1;36m' _c_cmd=$'\033[1;33m' _c_dim=$'\033[2m' _c_off=$'\033[0m'
else
  _c_say='' _c_cmd='' _c_dim='' _c_off=''
fi

# demo_init NAME  builds swim (unless SWIM_BIN is set), makes the scratch
# HOME and repo, and cds into the repo. Lane files in the demo's lanes/
# are copied into the repo by demo_lanes.
demo_init() {
  DEMO_NAME=$1
  # swim's approval socket lives under TMPDIR, and a unix socket path over
  # ~100 bytes doesn't bind (macOS's default TMPDIR is fine).
  if [ "${#TMPDIR}" -gt 60 ]; then export TMPDIR=/tmp; fi
  DEMO_SRC=$DEMOS/$DEMO_NAME
  DEMO_DIR=${TMPDIR:-/tmp}/swim-demos/$DEMO_NAME
  rm -rf "$DEMO_DIR"
  mkdir -p "$DEMO_DIR/bin" "$DEMO_DIR/repo" "$DEMO_DIR/cloud"

  if [ -n "${SWIM_BIN:-}" ]; then
    ln -s "$SWIM_BIN" "$DEMO_DIR/bin/swim"
  else
    printf '%sbuilding swim from %s ...%s\n' "$_c_dim" "$ROOT" "$_c_off"
    (cd "$ROOT" && go build -o "$DEMO_DIR/bin/swim" ./cmd/swim) || { echo "demo: go build failed" >&2; exit 1; }
  fi

  # Go's caches live under the real HOME; keep them there before swapping it.
  export GOCACHE GOMODCACHE
  GOCACHE=$(go env GOCACHE 2>/dev/null) GOMODCACHE=$(go env GOMODCACHE 2>/dev/null)

  export HOME=$DEMO_DIR/home XDG_CONFIG_HOME=$DEMO_DIR/home/.config
  export SWIM_BIN=$DEMO_DIR/bin/swim
  export PATH=$DEMO_DIR/bin:$DEMO_LIB/bin:$PATH
  export FAKECLOUD_HOME=$DEMO_DIR/cloud
  export PYTHONPATH=$ROOT/sdk/python${PYTHONPATH:+:$PYTHONPATH}   # import swim, for Python lanes
  export USER=operator SWIM_PAGER=cat
  export GIT_AUTHOR_NAME=operator GIT_AUTHOR_EMAIL=operator@demo.local
  export GIT_COMMITTER_NAME=operator GIT_COMMITTER_EMAIL=operator@demo.local
  unset SWIM_LANE SWIM_RUN SWIM_SOCKET SWIM_TOKEN STEP_LOG CI GITHUB_ACTIONS
  _demo_key operator "$HOME"

  cd "$DEMO_DIR/repo" || exit 1
  git init -q
  swim init >/dev/null
  echo
  printf '%sscratch repo: %s%s\n' "$_c_dim" "$DEMO_DIR/repo" "$_c_off"
}

# _demo_key WHO HOME  an unencrypted ed25519 key for one demo persona.
_demo_key() {
  mkdir -p "$2/.ssh"
  ssh-keygen -q -t ed25519 -N '' -C "$1@demo" -f "$2/.ssh/id_ed25519"
}

# demo_person NAME  a teammate with their own HOME and SSH key, for `as`.
demo_person() {
  _demo_key "$1" "$DEMO_DIR/people/$1"
}

# pubkey NAME  that persona's public key line ("operator" is you).
pubkey() {
  if [ "$1" = operator ]; then cat "$HOME/.ssh/id_ed25519.pub"
  else cat "$DEMO_DIR/people/$1/.ssh/id_ed25519.pub"; fi
}

# as NAME CMD...  run a command as a teammate: their HOME, config and key.
as() {
  local who=$1; shift
  HOME=$DEMO_DIR/people/$who XDG_CONFIG_HOME=$DEMO_DIR/people/$who/.config USER=$who "$@"
}

# demo_lanes [N...]  copy the demo's lanes/lane.N.* into the repo (all of
# them with no arguments).
demo_lanes() {
  local f
  if [ $# -eq 0 ]; then
    cp "$DEMO_SRC"/lanes/lane.* .
  else
    for f in "$@"; do cp "$DEMO_SRC"/lanes/lane."$f".* .; done
  fi
}

# demo_commit MSG  commit everything in the scratch repo (lookup.yml,
# approvals/, swim.yml ...), the way a team would share them.
demo_commit() {
  git add -A >/dev/null 2>&1 && git commit -qm "$1" >/dev/null 2>&1
  true
}

# scene TITLE  a heading for the next part of the story.
scene() {
  echo
  printf '%s━━ %s %s%s\n' "$_c_say" "$1" "$(printf '━%.0s' $(seq 1 $((60 - ${#1}))))" "$_c_off"
}

# say TEXT...  narration: what to point out to the room.
say() {
  local l
  for l in "$@"; do printf '%s%s%s\n' "$_c_say" "$l" "$_c_off"; done
}

# show 'CMD'  prints the command, then runs it (eval, so env prefixes and
# pipes work). Its exit code doesn't stop the demo.
show() {
  echo
  printf '%s$ %s%s\n' "$_c_cmd" "$1" "$_c_off"
  eval "$1"
  return 0
}

# pause  wait for Enter (skipped with DEMO_AUTO=1 or no terminal).
pause() {
  [ -n "${DEMO_AUTO:-}" ] && return 0
  [ -t 0 ] || return 0
  printf '%s[enter]%s' "$_c_dim" "$_c_off"
  read -r _
  printf '\033[1A\033[2K'
}

# demo_done  where the scratch repo is, for questions afterwards.
demo_done() {
  echo
  say "Done. The scratch repo is still there to poke at:"
  printf '  cd %s\n' "$DEMO_DIR/repo"
  printf '%s  (its HOME is %s: export HOME and XDG_CONFIG_HOME=$HOME/.config to use its keys)%s\n' "$_c_dim" "$DEMO_DIR/home" "$_c_off"
}
