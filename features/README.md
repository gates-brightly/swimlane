# features

Optional features, written down for implementation later. Nothing here is
built or promised.

**Frozen (2026-10-27):** no new features start until two weeks of my own
sessions show what to fix and cut ([T23](../dev/tasks.md#t23-friction-from-my-own-sessions-p1)).
A feature must save time, reduce risk or reduce cognitive load in actual
work. Finishing a feature
already in progress means closing out its open items, not extending it.
Fixes and cuts are fine. Each file is a spec: the problem, the proposed behaviour,
where it would land in the code, open questions, and how to test it.

| feature | file | status |
|---|---|---|
| (none yet) | | |

## Staging a feature

1. Add `features/<name>.md` with these sections:
   Summary, Motivation, Behaviour, Design, Open questions, Testing, Out of scope.
2. Add a row above with status `proposed`.
3. When work starts, set the status to `in progress` and link the branch or PR.
   Resolve the open questions in the spec first: answers go in the spec, not a chat.
4. When it ships, delete the spec and its row; git history keeps the design record.
   The user-facing docs are `swim guide` (`internal/help/guide.txt`), the README and `docs/` (the reference is `docs/usage.md`).

Statuses: `proposed`, `accepted`, `in progress`, `dropped (<why>)`.
