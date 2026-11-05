# swim helpers, module tool: the tools a lane needs on the operator's
# machine, at the versions it needs.
#
# Copy into the repo whose lanes use it (say lib/swim/tool.sh); in a lane:
#
#   # Includes: lib/swim/tool.sh
#   source lib/swim/tool.sh
#   stage check
#   tool_gate node 20+
#   tool_gate just
#
# Every function is tool_<name>. # Includes: pins the file with the lane,
# so approving the lane approves it as it is. Each gate runs its check
# through swim _helper (the check is swim's own code, as gate is); the log
# shows the call and what the check found. Nothing here changes anything.
# bash 3.2 and BSD tools (macOS).

# tool_gate CMD [VERSION | VERSION+]: a gate that CMD is on PATH, and
# with VERSION that `CMD --version` (or `CMD version` when that fails, as
# go's does) reports it: the first version number it prints is VERSION or
# starts with VERSION (node 20 matches v20.11.1; go 1.27 matches go1.27.1;
# 1.2 matches 1.2.0, not 1.20). VERSION+ is that version or later (node
# 20+ matches v20.11.1 and v22.3.0). The log shows where CMD is and what
# it says.
tool_gate() {
  gate --helper "tool_gate${*:+ $*}" --timeout 30s "${1:-tool_gate: a command}${2:+ $2}" "${SWIM_BIN:-swim}" _helper tool_gate "$@"
}
