# 0007. Config lives outside the repo, by repo and branch

- **Status:** Accepted
- **Date:** 2026-11-05
- **Source:** the cleanup review of 2026-11-05 (the committed `swim.yml`
  and `.swim.lock` deleted)

## Context

A config file in the repo is one any lane, worker or agent can edit:
the settings that limit what a lane may do (blocked commands, masked
secrets) would be in reach of what they limit. Repo files also made
swim's file handling more complicated than it needed to be.

## Decision

- All config is in `~/.config/swim/config.yml`; nothing in a repo
  configures swim, and a `swim.yml` left in one is ignored.
- Layers, the last that sets a key wins: `defaults`, `repos.NAME`,
  `repos.NAME/BRANCH`. NAME is the last part of the `origin` remote's
  URL (else the toplevel directory's name), so every worktree of a repo
  shares it.
- Agents never edit it; they give the operator the exact YAML.

## Consequences

- A setting can differ per repo and branch without a file in either.
- Settings don't travel with a clone: each operator has their own.

## See also

[Config](../reference/config.md).
