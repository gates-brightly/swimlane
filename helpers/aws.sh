# swim helpers, module aws: AWS. Read-only building blocks for lanes.
#
# Copy into the repo whose lanes use it (say lib/swim/aws.sh); in a lane:
#
#   # Includes: lib/swim/aws.sh
#   source lib/swim/aws.sh
#   stage check
#   aws_gate --profile sbx1 123456789012
#
# Every function is aws_<name>. # Includes: pins the file with the lane,
# so approving the lane approves it as it is. Each gate runs its check
# through swim _helper (the check is swim's own code, as gate is); the log
# shows the call and what the check found. Nothing here changes anything;
# destructive work stays in the lane, behind its own guard (lint doesn't
# read this file). bash 3.2 and BSD tools (macOS).

# aws_gate [--profile PROFILE] [ACCOUNT]: a gate that an AWS session is
# active (sts get-caller-identity answers, under PROFILE, else AWS_PROFILE
# if set), and with ACCOUNT that it is that account. The log shows who the
# session is; an expired SSO session's failure names the aws sso login
# that renews it.
aws_gate() {
  _aws_label=$(_aws_gate_label "$@")
  gate --helper "aws_gate${*:+ $*}" --timeout 30s "$_aws_label" "${SWIM_BIN:-swim}" _helper aws_gate "$@"
}

# _aws_gate_label ARGS: aws_gate's label, from its arguments.
_aws_gate_label() {
  _aws_p=
  if [ "${1:-}" = --profile ]; then
    _aws_p=${2:-?}
    shift
    [ $# -gt 0 ] && shift
  fi
  if [ -n "$_aws_p" ] && [ -n "${1:-}" ]; then
    echo "AWS session is $_aws_p, account $1"
  elif [ -n "$_aws_p" ]; then
    echo "AWS session is $_aws_p"
  elif [ -n "${1:-}" ]; then
    echo "AWS session is account $1"
  else
    echo "AWS session active"
  fi
}
