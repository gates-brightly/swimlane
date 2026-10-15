# features

Optional features, written down for implementation later. Nothing here is
built or promised. Each file is a spec: the problem, the proposed behaviour,
where it would land in the code, open questions, and how to test it.

| feature | file | status |
|---|---|---|
| Lane claims: a lease on a lane while a session writes it | [lane-claims.md](lane-claims.md) | proposed |
| swim observe: computed observations and knowledge from runs; optional Claude write-up | [observe.md](observe.md) | proposed |
| Modules: reusable Go and bash utilities for lanes, pinned like the script (built in, repo, fetched before approval) | [modules.md](modules.md) | proposed |
| Retry on a match: --retry-if REGEX, --backoff-max, --retry-for, swim.yml policies, stage and lane defaults (wait out Rush's lock) | [retry.md](retry.md) | proposed |
| swim pause / unpause (or resume): hold lanes at their next step, then resume or replace them | [pause.md](pause.md) | proposed |
| swim cache: tool download caches that outlive a round, checked against pinned lockfiles | [cache.md](cache.md) | in progress (phase one shipped in `754315c`; snapshots later, if measured) |
| Trash a lane: `d` in swim watch, a hub button, swim trash N (moved to .swim/cleaned/, restorable) | [trash.md](trash.md) | proposed |

## Staging a feature

1. Add `features/<name>.md` using the sections in the existing specs:
   Summary, Motivation, Behaviour, Design, Open questions, Testing, Out of scope.
2. Add a row above with status `proposed`.
3. When work starts, set the status to `in progress` and link the branch or PR.
   Resolve the open questions in the spec first: answers go in the spec, not a chat.
4. When it ships, delete the spec and its row; git history keeps the design record.
   The user-facing docs are `swim --help` (`internal/help/guide.txt`) and the README.

Statuses: `proposed`, `accepted`, `in progress`, `dropped (<why>)`.
