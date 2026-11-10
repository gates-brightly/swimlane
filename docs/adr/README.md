# Architecture decisions

One file per design decision that shapes swim: what was decided, the
alternatives rejected and what they would have cost, and what the choice
costs in turn. Read one before re-arguing it. Once a record is accepted
it isn't edited to say something else; a new one supersedes it, and the
old one's status says by which.

| # | Decision | Status |
|---|---|---|
| [0001](0001-the-agent-writes-the-operator-runs.md) | The agent writes, the operator runs | Accepted |
| [0002](0002-no-server-the-repo-is-the-channel.md) | No server: the repo is the only channel | Accepted |
| [0003](0003-bash-3-2-is-the-baseline.md) | bash 3.2 is the baseline | Accepted |
| [0004](0004-the-header-is-the-lanes-contract.md) | The header is the lane's contract | Accepted |
| [0005](0005-lanes-run-from-a-pinned-copy.md) | Lanes run from a pinned copy | Accepted |
| [0006](0006-guards-are-advisory.md) | Guards are advisory; only blocked commands are enforced | Accepted |
| [0007](0007-an-approval-is-used-once-by-its-lane.md) | An approval is used once, by the lane it was given to | Accepted |
| [0008](0008-keep-the-custody-record.md) | Keep the custody record, not the proof machinery | Accepted |
| [0009](0009-config-lives-outside-the-repo.md) | Config lives outside the repo, by repo and branch | Accepted |
| [0010](0010-one-syntax-version-frozen-at-4.md) | One syntax version, frozen at 4 | Accepted |
| [0011](0011-lint-warns-except-what-wouldnt-run.md) | Lint warns, except about what wouldn't run as it reads | Accepted |

**What belongs here:** a non-obvious design choice that would otherwise
be re-argued, with a real alternative. If a different design couldn't
supersede it, it's a value, and goes in [why swim](../reference/why.md).
Keep the decision, not the implementation: file paths, flags and lint
codes belong on the [reference](../reference/README.md) page it links.

New decision: copy [template.md](template.md) to the next number, fill in
every section (Alternatives considered too), and add it here.
