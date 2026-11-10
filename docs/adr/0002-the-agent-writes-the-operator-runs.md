# 0002. The agent writes, the operator runs

- **Status:** Accepted
- **Date:** 2026-10-09
- **Source:** swim's first commits; [why swim](../reference/why.md),
  [agents](../reference/agents.md)

## Context

AI sessions can write operational changes but shouldn't run them: they
lack the credentials, the network (VPN) or the authority. Pasting
commands out of a chat breaks quoting, skips steps and returns partial
output, and nothing records what ran or who approved it.

## Decision

- The agent (the planner and its workers) writes lane scripts; a person,
  or a watcher they run (`swim watch`), runs them with their own access.
- Assume two machines that share only the repo: the agent's sandbox, and
  the operator's machine (often macOS: bash 3.2, BSD tools).
- The agent never runs a round and never approves anything; it queues
  (`swim queue`, which approves nothing) and reads the logs back.

## Consequences

- Everything crosses through files in the repo: scripts, `.swim/`
  state, the queue, the logs. swim has no server.
- Lanes must run on the operator's machine, not the agent's: bash 3.2
  and BSD tool forms are linted for.
- The agent can't check a fact on the operator's side directly; it
  queues a lane of snapshot steps.

## See also

[Architecture](../reference/architecture.md),
[trust model](../reference/trust-model.md).
