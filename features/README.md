# features

Optional features, written down for implementation later. Nothing here is
built or promised. Each file is a spec: the problem, the proposed behaviour,
where it would land in the code, open questions, and how to test it.

| feature | file | status |
|---|---|---|
| Headless workers, driven from the hub | [headless.md](headless.md) | proposed |
| Execution manifest: approve and run one exact operation | [execution-manifest.md](execution-manifest.md) | proposed |
| Guarded secrets: approval enforced by credentials | [guarded-secrets.md](guarded-secrets.md) | proposed |
| Chain trust anchors: trusted signers and external checkpoints | [chain-anchors.md](chain-anchors.md) | proposed |

## Staging a feature

1. Add `features/<name>.md` using the sections in the existing specs:
   Summary, Motivation, Behaviour, Design, Open questions, Testing, Out of scope.
2. Add a row above with status `proposed`.
3. When work starts, set the status to `in progress` and link the branch or PR.
   Resolve the open questions in the spec first: answers go in the spec, not a chat.
4. When it ships, delete the spec and its row; git history keeps the design record.
   The user-facing docs are `swim --help` (`internal/help/guide.txt`) and the README.

Statuses: `proposed`, `accepted`, `in progress`, `dropped (<why>)`.
