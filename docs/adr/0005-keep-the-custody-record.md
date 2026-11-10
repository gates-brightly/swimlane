# 0005. Keep the custody record, not the proof machinery

- **Status:** Accepted
- **Date:** 2026-11-05
- **Source:** the cleanup review of 2026-11-05; signoff removed in aa2e50a

## Context

swim grew machinery to prove to someone else what ran: signoff, a
cache, approvals from others (`lookup.yml`), trust anchors (`trust.yml`,
chain anchors). The value people actually got was simpler: the exact
script that ran, its output and its approvals, kept after the session
that wrote them is gone. People running agents otherwise lean on the
cloud API's transcript to reconstruct what fired.

## Decision

- Keep the record: each round's script by hash, its log, the
  approvals, hash-linked in `.swim/chain.jsonl`.
- Remove signoff, the cache, approvals from others and trust anchors.
  Old chain fields stay so old entries still verify.
- Signing, run keys and attestation stay open questions; the docs don't
  claim the chain proves anything to an outside auditor.

## Consequences

- `swim log`, `swim verify` and the chain answer "what ran, and who
  approved it", locally.
- Approvals from other people, and proof to a third party, are out of
  scope until a new decision.

## See also

[Chain](../reference/chain.md), [trust model](../reference/trust-model.md).
