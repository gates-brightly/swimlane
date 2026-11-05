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
# so approving the lane approves it as it is. Each command runs through
# gate or snapshot, so the log records it. Nothing here changes anything;
# destructive work stays in the lane, behind its own guard (lint doesn't
# read this file). bash 3.2 and BSD tools (macOS).

# _aws_fail LABEL MESSAGE: a failed gate, for a function called wrong.
_aws_fail() {
  gate "$1" bash -c 'echo "$1"; exit 2' _ "$2"
}

# aws_gate [ACCOUNT]: a gate that an AWS session is active (sts
# get-caller-identity answers, under AWS_PROFILE if set), and with ACCOUNT
# that it is that account. The log shows who the session is.
aws_gate() {
  if [ -z "${1:-}" ]; then
    gate --helper "aws_gate" --timeout 30s "AWS session active" aws sts get-caller-identity --output json
    return
  fi
  case $1 in
    *[!0-9]*)
      _aws_fail "aws_gate: an account id" "aws_gate takes a 12-digit account id, got '$1'"
      return 1
      ;;
  esac
  gate --helper "aws_gate $1" --timeout 30s "AWS session is account $1" bash -c 'got=$(aws sts get-caller-identity --query Account --output text) || exit 1; echo "account $got (profile ${AWS_PROFILE:-default})"; [ "$got" = "$1" ] || { echo "want account $1"; exit 1; }' _ "$1"
}
