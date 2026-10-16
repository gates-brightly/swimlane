#!/usr/bin/env bash
# swim: syntax 4
# Round: words in quotes, comments and heredocs
# Job:   corpus-quoted
# Guards: ALLOW_REAL  the one real guard (2026-10-16, corpus)
# Timeout: 30m
#
# Every command and keyword lint looks for, named only where bash doesn't
# run it: double quotes, single quotes, heredocs, a string over two lines
# and comments. Lint reports nothing for this script. (git push and the
# other blocked commands are left out: swim refuses those on their text.)
_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found" >&2; exit 1; }
eval "$_swim_lib"
lane_init 1
stage snapshot
snapshot "list" ls
stage check
# comment: set -e; set -o errexit; declare -A m; mapfile -t x; ${x,,} |& &>>
# comment: guard ALLOW_X "x"; stage change; stage verify; summary; exit 1
# comment: lane_init 2; mod clone x; mod_env aws-session; source lib/x.sh
# comment: rm -rf /; terraform apply; kubectl delete pod x; aws s3 rm s3://b
# comment: npm install; pip install x; input 2 x; reached x; secret get TOKEN
# comment: AWS_PROFILE=prod; --profile prod; curl -X POST u; touch f; > out.txt
echo "set -e; set -o errexit; declare -A m; mapfile -t x; |& &>>"
echo "guard ALLOW_X x; stage change; stage verify; summary; exit 1"
echo "lane_init 2; mod clone x; mod_env aws-session; source lib/x.sh; . lib/y.sh"
echo "rm -rf /; terraform apply; kubectl delete pod x; drop table; destroy it"
echo "npm install; pip install x; input 2 x; reached x; swim secret get TOKEN"
echo "AWS_PROFILE=prod; --profile prod; curl -X POST u; touch f; cp a b; tee f"
echo 'set -e; declare -A m; mapfile -t x; ${x,,} |& &>>'
echo 'guard ALLOW_Y y; stage change; stage verify; summary; exit 1; lane_init 3'
echo 'mod clone x; mod_env aws-session; source lib/x.sh; rm -rf /tmp/x'
echo 'terraform destroy; helm uninstall x; npm i; input 3 y; secret get KEY'
echo 'AWS_PROFILE=prod; curl -X DELETE u; mkdir d; mv a b; > out.txt'
echo 'curl -H "token: $(swim secret get TOKEN)" u'
cat <<'EOF'
set -e
declare -A m
mapfile -t x
guard ALLOW_Z "z"
stage change
stage verify
summary
exit 1
lane_init 4
mod clone x
mod_env aws-session
source lib/x.sh
rm -rf /
terraform apply
kubectl delete pod x
npm install
pip install x
input 2 x
swim secret get TOKEN
AWS_PROFILE=prod aws s3 ls
curl -X POST u
touch f > out.txt
EOF
cat <<EOF
unquoted delimiter, still text: guard ALLOW_W "w"; stage change; rm -rf /
terraform apply; swim secret get TOKEN; input 2 x; mod clone x
EOF
echo "a string over
two lines: guard ALLOW_V v; stage change; rm -rf /; swim secret get TOKEN
input 2 x; mod clone x; set -e; terraform apply; kubectl delete pod x"
gate "precheck" test -d .
stage change
if guard ALLOW_REAL "the one real change (2026-10-16)"; then
  run "change" true
fi
stage verify
run "verify" true
summary
