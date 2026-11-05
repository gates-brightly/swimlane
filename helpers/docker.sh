# swim helpers, module docker: Docker on the operator's machine.
#
# Copy into the repo whose lanes use it (say lib/swim/docker.sh); in a lane:
#
#   # Includes: lib/swim/docker.sh
#   source lib/swim/docker.sh
#   stage snapshot
#   docker_compose_snapshot nxb-lith before
#   stage check
#   docker_gate
#
# Every function is docker_<name>. # Includes: pins the file with the lane,
# so approving the lane approves it as it is. Each gate runs its check
# through swim _helper (the check is swim's own code, as gate is); the log
# shows the call and what the check found. Nothing here changes anything.
# bash 3.2 and BSD tools (macOS).

# docker_gate: a gate that the Docker daemon answers (Docker Desktop is up).
# The log shows the client and server versions.
docker_gate() {
  gate --helper "docker_gate${*:+ $*}" --timeout 30s "docker is running" "${SWIM_BIN:-swim}" _helper docker_gate "$@"
}

# docker_compose_snapshot PROJECT [WHEN]: snapshot every container of
# compose project PROJECT, running or not ("PROJECT containers WHEN";
# WHEN as in before / after).
docker_compose_snapshot() {
  if [ -z "${1:-}" ]; then
    gate "docker_compose_snapshot: a project" bash -c 'echo "docker_compose_snapshot needs a compose project name"; exit 2'
    return 1
  fi
  snapshot --helper "docker_compose_snapshot${*:+ $*}" "$1 containers${2:+ $2}" docker ps -a --filter "label=com.docker.compose.project=$1" --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}'
}
