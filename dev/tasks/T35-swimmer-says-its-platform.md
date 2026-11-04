# T35. The swimmer says what platform lanes run on (P2, gap)

Source: agents in a Linux sandbox writing lanes that a macOS operator runs.
The skill's "Two machines" section now tells them to assume that split,
but they learn it from prose, not from swim.

**Today:** a swimmer records itself in each batch it takes as a
`queue.Worker` (internal/queue/queue.go:63): host, pid, user, start time
and heartbeat, but not its OS, architecture or bash. `swim queue` and
`swim status` name the swimmer's host at most
(internal/cli/queue.go:735). An agent's `swim env` and `swim doctor`
describe the agent's own machine, so nothing tells it that the lanes it
writes run on darwin with bash 3.2.

**Do:**
- Record the swimmer's platform with its heartbeat: GOOS/GOARCH and the
  `bash --version` its lanes get.
- Show it where an agent already looks before handing over: `swim queue`
  (`swimmer: zach-mbp, darwin/arm64, bash 3.2`) and `swim status`'s
  queue line, and in `--yaml`.
- With no swimmer up, show the last one seen, marked as such.

**Done when:** an e2e test with a second "host" (as queue_test.go plays
one) shows the other host's platform in `swim queue` and `swim status
--yaml`, and the skill's "Two machines" section points at it instead of
saying "often macOS".
