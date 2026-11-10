# 0001. The agent writes, the operator runs

- **Status:** Accepted
- **Date:** 2026-10-09
- **Source:** swim's first commits; [why swim](../reference/why.md)

## Context

AI sessions can write operational changes but shouldn't run them: they
lack the credentials, the network (VPN) or the authority. Pasting
commands out of a chat breaks quoting, skips steps and returns partial
output, and nothing records what ran or who approved it.

## Decision

- The agent writes lane scripts; a person, or a watcher they run, runs
  them on their own machine with their own access.
- The agent never runs a round and never approves anything. It can queue
  work, which approves nothing, and it reads the logs back.

## Alternatives considered

- **Let the agent run with scoped credentials** (short-lived tokens, a
  role per task): no round trip through a person. But scoping each task
  ahead is the hard part, a VPN or SSO session can't be scoped that way,
  and the person learns what ran afterwards instead of approving it
  before.
- **The agent proposes commands one at a time in chat:** nothing to
  install. It's the problem swim exists to fix: quoting, skipped steps,
  partial output, no record.
- **A runbook server the agent submits to** (Rundeck, AWX): approvals and
  a record, but a server to run, and curated runbooks rather than this
  morning's one-off script. See [0002](0002-no-server-the-repo-is-the-channel.md).

## Consequences

- The agent can't check a fact on the operator's side directly; it
  writes a lane of read-only steps and reads the log.
- Lanes run on a machine the agent can't test on
  ([0003](0003-bash-3-2-is-the-baseline.md)).
- Each round costs the operator a look; the queue and the watcher keep
  that to one look per batch.

## See also

[Architecture](../reference/architecture.md),
[agents](../reference/agents.md).
