#!/usr/bin/env bash
# swim's git shim. lane_init puts .swim/bin first on the lane's PATH, so every
# `git` a lane runs (in run/gate steps, bare in the script, or in scripts it
# calls) comes here first. swim never pushes, commits or pulls: those are
# refused (exit 87) and the round is stopped; every other call goes to the
# real git. Bash 3.2 compatible.
shim_dir=$(cd "$(dirname "$0")" && pwd -P)

# The subcommand is the first argument that isn't a global option.
sub=
skip=0
for a in "$@"; do
  if [ "$skip" = 1 ]; then
    skip=0
    continue
  fi
  case "$a" in
    -C|-c|--git-dir|--work-tree|--namespace|--exec-path|--config-env|--super-prefix) skip=1 ;;
    -*) ;;
    *) sub=$a; break ;;
  esac
done

case "$sub" in
  push|commit|pull)
    echo "swim: \`git $sub\` is blocked: swim never pushes, commits or pulls" >&2
    if [ -n "${SWIM_BIN:-}" ] && [ -n "${SWIM_LANE:-}" ]; then
      "$SWIM_BIN" _mark "$SWIM_LANE" BLOCKED "git $sub" "git shim; swim never writes to git" >&2
      if [ -n "${SWIM_PID:-}" ]; then
        kill -USR1 "$SWIM_PID" 2>/dev/null
      fi
    fi
    exit 87 ;;
esac

# Hand off to the real git: the first git on PATH that isn't this shim.
old_ifs=$IFS
IFS=:
for d in $PATH; do
  IFS=$old_ifs
  [ -n "$d" ] || continue
  if [ "$(cd "$d" 2>/dev/null && pwd -P)" = "$shim_dir" ]; then
    continue
  fi
  if [ -x "$d/git" ] && [ ! -d "$d/git" ]; then
    exec "$d/git" "$@"
  fi
done
echo "swim: git not found on PATH" >&2
exit 127
