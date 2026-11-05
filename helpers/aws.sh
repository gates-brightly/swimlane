# swim helpers, module aws: AWS. Read-only building blocks for lanes.
#
# Copy into the repo whose lanes use it (say lib/swim/aws.sh); in a lane:
#
#   # Includes: lib/swim/aws.sh
#   source lib/swim/aws.sh
#   stage check
#   aws_gate 123456789012
#
# Every function is aws_<name>. # Includes: pins the file with the lane,
# so approving the lane approves it as it is. Each gate runs its check
# through swim _helper (the check is swim's own code, as gate is); the log
# shows the call and what the check found. Nothing here changes anything;
# destructive work stays in the lane, behind its own guard (lint doesn't
# read this file). bash 3.2 and BSD tools (macOS).

# aws_gate [ACCOUNT]: a gate that an AWS session is active (sts
# get-caller-identity answers, under AWS_PROFILE if set), and with ACCOUNT
# that it is that account. The log shows who the session is.
aws_gate() {
  gate --helper "aws_gate${*:+ $*}" --timeout 30s "AWS session ${1:+is account $1}${1:-active}" "${SWIM_BIN:-swim}" _helper aws_gate "$@"
}
