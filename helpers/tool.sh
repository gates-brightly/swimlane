# swim helpers, module tool: the tools a lane needs on the operator's
# machine, at the versions it needs.
#
# Copy into the repo whose lanes use it (say lib/swim/tool.sh); in a lane:
#
#   # Includes: lib/swim/tool.sh
#   source lib/swim/tool.sh
#   stage check
#   tool_gate node 20
#   tool_gate just
#
# Every function is tool_<name>. # Includes: pins the file with the lane,
# so approving the lane approves it as it is. Nothing here changes
# anything. bash 3.2 and BSD tools (macOS).

# tool_gate CMD [VERSION]: a gate that CMD is on PATH, and with VERSION
# that `CMD --version` (or `CMD version` when that fails, as go's does)
# reports it: the first version number it prints is VERSION or starts with
# VERSION. (node 20 matches v20.11.1; go 1.27 matches go1.27.1; 1.2
# matches 1.2.0, not 1.20.) The log shows where CMD is and what it says.
tool_gate() {
  if [ -z "${1:-}" ]; then
    gate "tool_gate: a command" bash -c 'echo "tool_gate needs a command name"; exit 2'
    return 1
  fi
  gate --timeout 30s "$1${2:+ $2}" bash -c 'p=$(command -v "$1") || { echo "$1: not on PATH"; exit 1; }; v=$("$1" --version 2>&1) || v=$("$1" version 2>&1); v=$(printf "%s\n" "$v" | head -1); echo "$p: $v"; [ -n "$2" ] || exit 0; n=$(printf "%s\n" "$v" | grep -oE "[0-9]+(\.[0-9]+)*" | head -1); case $n in "$2" | "$2".*) exit 0 ;; esac; echo "want $1 $2, got ${n:-no version}"; exit 1' _ "$1" "${2:-}"
}
