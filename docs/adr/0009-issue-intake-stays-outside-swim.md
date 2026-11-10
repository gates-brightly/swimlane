# 0009. Issue intake stays outside swim

- **Status:** Accepted
- **Date:** 2026-11-03
- **Source:** a GitHub-issues inbox in `swim watch` (`swim inbox`, a
  dispatch to a template lane) built and reverted on 2026-11-03

## Context

Work often starts as a GitHub issue. Building an inbox into `swim watch`
made swim talk to the network and to GitHub, and added concepts before
anyone asked for them.

## Decision

- swim stays free of GitHub, issue-tracker and other network
  integrations (beyond notifications).
- Issue intake, if built, is its own component: it listens for issues and
  writes prompts for agents, which then use swim as usual.

## Consequences

- swim remains a tool, not a platform: one binary, files in the repo.
- Getting from an issue to a lane is the agent's job, not swim's.

## See also

[0008](0008-swim-is-a-personal-tool.md).
