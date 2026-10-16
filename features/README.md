# features

Optional features, written down for implementation later. Nothing here is
built or promised. Each file is a spec: the problem, the proposed behaviour,
where it would land in the code, open questions, and how to test it.

| feature | file | status |
|---|---|---|
| swim observe: computed observations and knowledge from runs; an optional write-up with the claude module | [observe.md](observe.md) | proposed |
| Modules: reusable Go and bash utilities for lanes, pinned like the script (built in, repo, fetched before approval) | [modules.md](modules.md) | in progress (built in and repo modules and ten built-ins shipped; fetched modules and a few gaps left) |
| Retry on a match: --retry-if REGEX, --backoff-max, --retry-for, swim.yml policies, stage and lane defaults (wait out Rush's lock) | [retry.md](retry.md) | dropped (matching retries on output with regexes isn't wanted) |

## Staging a feature

1. Add `features/<name>.md` using the sections in the existing specs:
   Summary, Motivation, Behaviour, Design, Open questions, Testing, Out of scope.
2. Add a row above with status `proposed`.
3. When work starts, set the status to `in progress` and link the branch or PR.
   Resolve the open questions in the spec first: answers go in the spec, not a chat.
4. When it ships, delete the spec and its row; git history keeps the design record.
   The user-facing docs are `swim --help` (`internal/help/guide.txt`) and the README.

Statuses: `proposed`, `accepted`, `in progress`, `dropped (<why>)`.
