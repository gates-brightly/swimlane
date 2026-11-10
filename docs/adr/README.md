# Architecture decisions

One file per decision that shapes swim: what was decided, why, and what
it costs. Read one before re-arguing it; supersede it rather than editing
it once it's accepted.

| # | Decision | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-the-agent-writes-the-operator-runs.md) | The agent writes, the operator runs | Accepted |
| [0003](0003-the-header-is-the-lanes-contract.md) | The header is the lane's contract | Accepted |
| [0004](0004-lanes-run-from-a-pinned-copy.md) | Lanes run from a pinned copy | Accepted |
| [0005](0005-keep-the-custody-record.md) | Keep the custody record, not the proof machinery | Accepted |
| [0006](0006-one-syntax-version-frozen-at-4.md) | One syntax version, frozen at 4 | Accepted |
| [0007](0007-config-lives-outside-the-repo.md) | Config lives outside the repo, by repo and branch | Accepted |
| [0008](0008-swim-is-a-personal-tool.md) | swim is a personal tool | Accepted |
| [0009](0009-issue-intake-stays-outside-swim.md) | Issue intake stays outside swim | Accepted |
| [0010](0010-lanes-are-slots-found-on-disk.md) | Lanes are slots found on disk | Accepted |
| [0011](0011-bash-3-2-is-the-baseline.md) | bash 3.2 is the baseline | Accepted |
| [0012](0012-an-approval-is-used-once-by-its-lane.md) | An approval is used once, by the lane it was given to | Accepted |
| [0013](0013-ci-runs-committed-lanes.md) | CI runs committed lanes through swim ci | Accepted |
| [0014](0014-swim-never-writes-to-git.md) | swim never writes to git; lanes may, behind a guard | Accepted |

New decision: copy [template.md](template.md) to the next number, and add
it here.
