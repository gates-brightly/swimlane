# features

Optional features, written down for implementation later. Nothing here is
built or promised.

**Frozen (2026-10-27):** no new features start until outside users have
run swim ([T23](../tasks.md#t23-outside-feedback-p1)). Finishing a feature
already in progress means closing out its open items, not extending it.
Fixes and cuts are fine. Each file is a spec: the problem, the proposed behaviour,
where it would land in the code, open questions, and how to test it.

| feature | file | status |
|---|---|---|
| Sandboxed lanes: a throwaway copy of the repo per lane, so concurrent jobs don't clobber each other's files (only $SWIM_OUT is kept); and a container with its network off or allowlisted, opened per stage only once its guard is approved; image and policy signed in the manifest; no access to the operator's keys | [sandbox.md](sandbox.md) | proposed |

## Staging a feature

1. Add `features/<name>.md` using the sections in the existing specs:
   Summary, Motivation, Behaviour, Design, Open questions, Testing, Out of scope.
2. Add a row above with status `proposed`.
3. When work starts, set the status to `in progress` and link the branch or PR.
   Resolve the open questions in the spec first: answers go in the spec, not a chat.
4. When it ships, delete the spec and its row; git history keeps the design record.
   The user-facing docs are `swim guide` (`internal/help/guide.txt`) and the README.

Statuses: `proposed`, `accepted`, `in progress`, `dropped (<why>)`.
