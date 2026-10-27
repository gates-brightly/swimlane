# Sandboxed lanes: an isolated workspace, and network as a guarded capability

Status: proposed. Nothing here is built.

## Summary

A lane may declare that it runs in a container, with its network cut off
or limited to an allowlist:

```bash
# Sandbox: docker ghcr.io/acme/ops-tools@sha256:4f1c…
# Workspace: ephemeral
# Network: none
# Network: allow api.cloudflare.com:443, 10.0.0.0/8
```

Two kinds of isolation, usable together or apart:

- **Workspace** (`# Workspace: ephemeral`): the lane works in a throwaway
  copy of the repo. What it writes there (build output, `go build` caches,
  generated files, a half-applied migration) is discarded when the round
  ends, so concurrent swim jobs never see or clobber each other's files.
  Only what it puts in `$SWIM_OUT` (`# Outputs:`) is kept. "Test that it
  compiles, then throw the result away" is a lane with no outputs.

The snapshot, check and plan stages run with the network off. The change
stage gets its allowlist only once its guard is approved, so a dry run
physically can't make the call it is planning. The image digest and the
network policy are part of the execution manifest, so widening the
allowlist or changing the image stales the approval, as editing the
script does. The container sees only the repo and the credentials given to
the lane, so a lane can no longer read the runner's signing key or
`~/.ssh`.

## Motivation

- Concurrent lanes share one working tree. Two lanes building at once
  fight over `bin/`, `dist/`, `node_modules/`, `.terraform/` and caches;
  a lane that edits files (codegen, `go mod tidy`, a formatter) changes
  what another lane is reading mid-run. Today the only answer is
  `# Locks:`, which serializes them. An ephemeral workspace lets them run
  side by side, and makes "did it build?" checks free of side effects.

- An AI-written script has two dangerous channels: its side effects and
  its network. Guards and dry runs control the first. Today a lane can
  `curl` anything at any point, including in the read-only snapshot and
  check stages, so "this lane only looked" is a promise, not a proof.
- Fully air-gapping is too narrow: most ops lanes need some network (the
  API they change, the host they ssh to, a package mirror). The useful
  primitive is a declared, signed network policy, unlocked by approval.
- Credential isolation comes with it. The README's biggest caveat (a lane
  runs as you, so it can use the runner key wherever you can without
  being asked) goes away for sandboxed lanes: nothing from the operator's
  home is mounted, and no agent socket.

## Behaviour

- `# Workspace: shared | ephemeral` (default `shared`, today's behaviour):
  - `ephemeral`: before the lane starts, swim makes a copy-on-write copy
    of the repo as the run staged it (tracked files plus the working
    tree's uncommitted changes, the same snapshot the manifest pins) and
    runs the lane there. `SWIM_ROOT` and the working directory point at
    the copy; `.swim/` state (status, logs, approvals) stays the real
    repo's, through swim, not through the copy.
  - When the round ends, pass or fail, the copy is deleted. `$SWIM_OUT`
    lives outside it (`.swim/out/<job>/<run>/`, as today), so declared
    outputs survive and dependents read them with `input N NAME`.
  - `# Workspace: ephemeral` doesn't need `# Sandbox:`: without a
    container it is a plain directory copy, so it works on any host. With
    a container, the copy is what's mounted.
  - `swim run --keep-workspace` keeps the copy for debugging and prints
    its path; `swim lint` warns when an ephemeral lane writes to a path
    outside `$SWIM_OUT` that a later lane in the same run reads.
  - A lane with `# Workspace: ephemeral` needs no `# Locks:` for files in
    the repo (lint says so); locks for outside resources still apply.

- `# Sandbox: docker IMAGE` runs the lane's process in a container from
  IMAGE. A tag is resolved to its digest at plan time and the digest is
  what's pinned and run; `swim lint` warns on an unpinned tag.
- `# Network:` (only with `# Sandbox:`):
  - `none` (the default for a sandboxed lane): no network at all.
  - `host`: the host's network, unrestricted (an explicit opt-out,
    recorded, and flagged by `swim lint` and `swim doctor`).
  - `allow ENTRY, ...`: only these destinations. An entry is a DNS name
    (`api.cloudflare.com`), a wildcard of one label (`*.s3.amazonaws.com`),
    an IP or CIDR (`10.0.0.0/8`, `192.168.1.20`), each with an optional
    port or port list (`:443`, `:22,443`; default any). For example:
    `# Network: allow api.github.com:443, *.s3.us-east-1.amazonaws.com:443, 10.0.0.0/8:22`.
  - DNS is controlled too: the lane's only resolver is swim's, which
    answers allowed names and returns NXDOMAIN (logged) for the rest, so a
    lane can't even learn the address of a host it may not reach.
- Per stage: snapshot, check and evidence steps run with `none` whatever
  the policy says; the allowlist opens for the change and verify stages
  once a guard in the round is approved (a round with no guard gets it for
  its change stage at once, as today's lanes get everything).
- A blocked connection is a `BLOCKED  connect evil.example:443 (not in #
  Network:)` line in the lane log, shown on the hub; the step's command
  sees a refused connection, not a hang.
- Mounted: the repo (read-write, at the same path), the swim binary and
  lane library (read-only), the approval socket, and each credential the
  lane is given as an environment variable or a file in a tmpfs. Nothing else from the
  operator's home.
- `swim plan` and the hub's Runner/run pages show the image digest and the
  network policy for each sandboxed lane; `swim verify` and the receipt
  include them.

## Design

- Workspace copy (`internal/launcher`, next to staging in `stage.go`):
  `git worktree add --detach` at the staged commit, then the working
  tree's uncommitted changes applied, or a reflink copy (`cp -c` on APFS,
  `cp --reflink=auto` on btrfs/XFS) falling back to a plain copy, with
  `.git`, `.swim/` and ignored build output left out. Created under
  `.swim/workspaces/<run>/<lane>/`, removed at round end and by `swim
  clean`. Large repos: copy-on-write keeps it cheap; measure the plain
  copy fallback before making it the default anywhere.
- Inside a container the copy is the bind mount, so the container's
  writes never reach the real tree. An overlayfs (the repo as a read-only
  lower layer, a tmpfs upper) is an alternative on Linux when the daemon
  allows it.

- Header parsing next to `# Includes:` (`internal/lane/lane.go` Info):
  `Sandbox` and `Network` fields; script syntax bump (new headers), with
  `swim lint --fix` unaffected for lanes that don't use them.
- Manifest (`internal/manifest`): two new pinned fields, `sandbox: docker
  <digest>` and `network: <canonical policy>`, written only when set, so
  existing manifests hash the same (depends on the pin-stability work:
  "a new field affects the hash only when set").
- Launcher (`internal/launcher`): a sandboxed lane's process is started as
  `docker run --rm --network <net> -v repo -v swim -v approval-socket
  --user $(id -u):$(id -g) IMAGE <runner> lane.N.sh`, with the lane's
  environment passed explicitly (no inherited `~`, `SSH_AUTH_SOCK`,
  cloud credential variables).
- DNS and IP controls (phase two), two layers so neither can be skipped:
  - **Resolver.** The container's `--dns` is a small resolver swim runs per
    run on the sandbox network. It forwards queries for allowed names
    (exact or one-label wildcard) to the host's resolver, answers NXDOMAIN
    for everything else, and records each answer: name → IPs, with TTL.
    DNS-over-HTTPS and DNS-over-TLS are just blocked egress (below), so
    they can't route around it.
  - **Egress filter.** The container's network has no default route out;
    its only way out is swim's gateway (a sidecar with `NET_ADMIN`, or the
    proxy below), which allows a connection only to (a) an IP the
    resolver handed out for an allowed name, while that answer's TTL
    lasts (plus a grace), on an allowed port, or (b) an allowed IP/CIDR
    and port. A lane that connects to a hard-coded IP not in the list is
    refused, and that's the `BLOCKED` line.
  - **TLS check.** Shared IPs (CDNs, cloud load balancers) mean "this IP
    was resolved for api.github.com" doesn't prove the connection is to
    api.github.com. For TCP 443 the gateway also reads the TLS
    ClientHello's SNI and refuses a name that isn't allowed (no
    decryption; ECH, when it arrives, would need its outer name allowed).
  - **Simplest form first.** An HTTP(S) CONNECT proxy (`HTTPS_PROXY`
    set in the lane's environment) enforces names directly and covers
    most API clients; the gateway covers tools that ignore proxies (ssh,
    database clients, anything with raw sockets). Ship the proxy plus
    `--network none`-with-only-the-proxy first; add the transparent
    gateway when a lane needs non-HTTP egress.
  - Stage-gated: resolver and gateway refuse everything (beyond what the
    snapshot and check stages are allowed) until the launcher tells them
    the round's guard was approved; denials go to the launcher for the
    lane's log.
- Approval broker: `peer.FromLane` (`internal/peer/peer.go`) checks the
  requester is in the lane's process tree, which a container's PID
  namespace hides. Options: `--pid=host`, or accept the lane token alone
  for sandboxed lanes (weaker; documented in the trust model).
- `swim doctor`: Docker present and reachable, image pulled, the proxy's
  network creatable.

## Open questions

- IPv6: allow-listing has to cover AAAA answers and v6 CIDRs, or the
  sandbox network should be v4-only at first (simpler, and says so).
- Names allowed only by IP-range wildcards (cloud provider ranges) are
  big and change: support importing a published range list
  (`allow aws:s3:us-east-1`), or leave it to the lane author?

- What the copy includes: tracked files plus uncommitted changes
  (matches what the manifest pins), or the whole tree including ignored
  files (`node_modules/` could be wanted, or a 2 GB waste)? Perhaps
  `# Workspace: ephemeral` plus `# Workspace-Include: node_modules`.
- Caches across runs: an ephemeral build is slow if every run starts
  cold. Mount swim's tool caches (`# Cache:`) into the copy read-write,
  outside the throwaway part?
- Should a lane be able to promote its workspace changes back on success
  (`# Workspace: ephemeral, apply-on-pass`), e.g. a formatter? That makes
  the change a reviewable diff instead of a direct write, but it's a
  second feature.

- PID namespace vs the broker's process check: `--pid=host`, a token-only
  path, or passing a socket the container can't forward?
- Do snapshot and check stages need any network (read-only API calls are
  common: `terraform plan`, `aws describe-*`)? Maybe `# Network-Read:
  allow ...` for those stages, separate from the change stage's.
- Docker only, or a Docker-free Linux backend (bubblewrap or a network
  namespace via `unshare`) for hosts without Docker? macOS has no
  network namespaces, so Docker Desktop would be required there.
- File ownership on the repo mount (user namespaces, `--user`), and
  rootless Docker/Podman.
- How the act module (running GitHub workflows) shares this plumbing:
  act already runs jobs in containers.

## Testing

- DNS and IP controls: an allowed name resolves and connects; a name not
  allowed gets NXDOMAIN and a `BLOCKED  resolve evil.example` line; a
  connection to a hard-coded IP not in the list is refused, as is a
  DNS-over-HTTPS query to 1.1.1.1; on 443, a ClientHello naming a host
  not allowed is refused even when its IP is shared with an allowed one.

- Two concurrent `# Workspace: ephemeral` lanes run `go build -o bin/x`
  with different sources; neither sees the other's `bin/x`, the real
  tree has no `bin/x` afterwards, and a declared output survives in
  `$SWIM_OUT`.
- A failing ephemeral lane leaves no files behind; `--keep-workspace`
  keeps and names the copy.

- e2e (skipped without Docker): a `# Network: none` lane's `curl` fails
  with a `BLOCKED` line; an allowlisted host works only after its guard
  is approved; the dry run of the change stage makes no connection.
- The runner key and `~/.ssh` aren't readable inside a sandboxed lane.
- Changing the image digest or the allowlist stales the approval; a lane
  without the headers hashes exactly as before (golden manifest test).
- `swim verify` and the hub show the image digest and network policy.

## Out of scope

- Syscall sandboxing (seccomp) and a read-only repo except declared
  paths: the ephemeral workspace already keeps writes out of the real
  tree.
- Building images: the lane names one; swim doesn't build it.
- Windows.
