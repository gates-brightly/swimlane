# swim helpers, module os: the machine a lane runs on. For lanes that
# must, or must not, run on the operator's Mac or a Linux box.
#
# Copy into the repo whose lanes use it (say lib/swim/os.sh); in a lane:
#
#   # Includes: lib/swim/os.sh
#   source lib/swim/os.sh
#   stage check
#   os_mac_gate
#   if os_linux; then step "..." ...; fi
#
# Every function is os_<name>. # Includes: pins the file with the lane, so
# approving the lane approves it as it is. Nothing here changes anything.
# bash 3.2 and BSD tools (macOS).

# os_mac / os_linux: true on macOS / Linux (uname -s), for branching. They
# log nothing; the gates log what the machine is.
os_mac() {
  [ "$(uname -s)" = Darwin ]
}

os_linux() {
  [ "$(uname -s)" = Linux ]
}

# os_mac_gate / os_linux_gate: a gate that this is macOS / Linux. The log
# shows the system (uname -srm).
os_mac_gate() {
  gate --helper "os_mac_gate" "running on macOS" bash -c 'uname -srm; [ "$(uname -s)" = Darwin ] || { echo "not macOS"; exit 1; }'
}

os_linux_gate() {
  gate --helper "os_linux_gate" "running on Linux" bash -c 'uname -srm; [ "$(uname -s)" = Linux ] || { echo "not Linux"; exit 1; }'
}
