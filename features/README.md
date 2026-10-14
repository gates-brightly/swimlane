# features

Optional features, written down for implementation later. Nothing here is
built or promised. Each file is a spec: the problem, the proposed behaviour,
where it would land in the code, open questions, and how to test it.

| feature | file | status |
|---|---|---|
| Native SDKs: Python, TypeScript and Go libraries for lane steps, generated from a `swim.lane.v1` protobuf contract and served over a per-lane socket; all decisions stay in the binary | [native-sdk.md](native-sdk.md) | proposed |
| Read-only preview: `swim plan --check` runs each round's `snapshot` and `check` stages against live state and stops before `change`; the plan shows which gates pass | [plan-check.md](plan-check.md) | proposed |
| Plan approvals: `evidence` steps (e.g. `terraform plan`) are signed with the approval and re-compared before `change`; not checked in `swim watch` | [plan-approvals.md](plan-approvals.md) | proposed |
| Matrix rounds: `# Matrix: SVC=a,b,c` runs one lane script once per value, each cell with its own log, status, result and retry | [matrix-rounds.md](matrix-rounds.md) | proposed |
| Pre-approve in the live view: select a lane with `←`/`→` and press `y` (or `n`) to answer its guards before it asks; pinned to the script, this run only | [preapprove.md](preapprove.md) | proposed |

## Staging a feature

1. Add `features/<name>.md` using the sections in the existing specs:
   Summary, Motivation, Behaviour, Design, Open questions, Testing, Out of scope.
2. Add a row above with status `proposed`.
3. When work starts, set the status to `in progress` and link the branch or PR.
   Resolve the open questions in the spec first: answers go in the spec, not a chat.
4. When it ships, delete the spec and its row; git history keeps the design record.
   The user-facing docs are `swim --help` (`internal/help/guide.txt`) and the README.

Statuses: `proposed`, `accepted`, `in progress`, `dropped (<why>)`.
