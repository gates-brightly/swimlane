# 0002. No server: the repo is the only channel

- **Status:** Accepted
- **Date:** 2026-10-27 (the hub removed); issue intake kept out
  2026-11-03
- **Source:** b09cdee "Remove the hub, headless swimmers and swim auth";
  a GitHub-issues inbox in `swim watch`, built and reverted 2026-11-03

## Context

The agent and the operator are on two machines (0001): the agent's
sandbox and the operator's laptop. Something has to carry scripts one
way and logs and answers the other. Both machines already share the
repo's checkout, and nothing else is guaranteed: the sandbox often has
no route to the laptop, and the laptop is often behind a VPN.

## Decision

- swim is one binary with no server and no daemon. The agent and the
  operator exchange only files in the repo: lane scripts, the queue,
  status and logs.
- The watcher is a process the operator starts in a terminal and stops;
  nothing runs when no one started it.
- swim talks to no network service of its own. Notifications are the
  exception, and they carry a question out to the operator, never work
  in.
- Getting work in from elsewhere (an issue tracker, a chat) is a separate
  tool that writes prompts for an agent, which then uses swim as usual.

## Alternatives considered

- **A hub server** that relays lanes, answers and logs between machines,
  with users, roles and a web app. Built, then removed on 2026-10-27: it
  meant TLS, logins, sessions and its own chain anchor to run and secure,
  for one operator who already shares a checkout with the agent.
- **A background service** (headless watchers installed as a service):
  rounds run without a terminal open. Removed with the hub: an unwatched
  runner has no one to answer a guard, and it's a second thing to keep
  alive and to secure.
- **An inbox for GitHub issues in the watcher:** work starts where it's
  filed. Built and reverted on 2026-11-03: it made swim talk to GitHub
  and added ideas before anyone needed them.
- **The agent reaches the operator's machine directly** (SSH, an MCP
  tool): no files in between. It needs a route the sandbox usually
  hasn't got, and gives the agent a way to run things (0001).

## Consequences

- Install is a binary; there's nothing to host, secure or keep running.
- Handoff is as fast as the files are: the agent polls, or waits on the
  queue.
- A watcher answering phone taps listens on the operator's own tailnet
  while it runs. That's the operator answering their own watcher, not a
  channel for the agent.
- Anything multi-user (a shared dashboard, approvals across a team) is
  out until a new decision.

## See also

[Architecture](../reference/architecture.md), [queue](../reference/queue.md),
[why swim](../reference/why.md).
