# 0001. Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-11-10
- **Source:** this directory

## Context

swim's reference docs (`docs/reference/`) say how swim works as built.
They don't say why: why the header and not the body is the contract, why
lanes run from a pinned copy, why the custody record stayed when signoff
went. That lives in commit messages, reviews and session notes, and each
review re-opens it.

## Decision

Record each decision that shapes swim as a short file here, numbered, in
the [template](template.md): context, decision, consequences. An accepted
one isn't edited to say something else; a new one supersedes it, and the
old one's status says by which.

## Consequences

- A review, or an agent session, can check the record before proposing to
  undo a decision.
- The reference pages stay about what is; these say why, and link there.
- Decisions recorded after the fact (0002 to 0010) carry the date they
  were made, from the commit or note that made them.

## See also

[Architecture](../reference/architecture.md), [why swim](../reference/why.md).
