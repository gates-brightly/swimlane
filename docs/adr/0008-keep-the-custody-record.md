# 0008. Keep the custody record, not the proof machinery

- **Status:** Accepted
- **Date:** 2026-11-05
- **Source:** the cleanup review of 2026-11-05; signoff removed in aa2e50a

## Context

swim grew machinery to prove to someone else what ran: signoff, a cache,
approvals from others, trust anchors outside the machine. The value
actually used was simpler: the exact script that ran, its output and its
approvals, kept after the session that wrote them is gone. Without it,
people running agents fall back on a transcript to reconstruct what
fired.

## Decision

- Keep the record: each round's script by hash, its log and its
  approvals, in a hash-linked chain that verify checks. Signing stays as
  built: entries are SSH-signed, and each run's entries by a run key the
  runner key vouches for.
- Remove signoff, the cache, approvals from others and trust anchors.
  Old entries that use them still verify.
- Further investment in proof to a third party (attestation others can
  check, keys held off the machine) is undecided.

## Alternatives considered

- **Keep the proof machinery:** a record an auditor could rely on. But
  it had no recorded use, was a large share of swim's code, and still
  can't prove much while the runner key and the chain sit on one
  machine.
- **Drop the chain, keep the logs:** less to build. But a log alone
  doesn't tie the output to the script that made it or to who approved
  it, and an edit goes unnoticed.
- **Rely on the agent's transcript:** nothing to build. It records what
  the agent wrote, not what the operator ran, and it goes when the
  session does.

## Consequences

- Log, verify and the chain answer "what ran, and who approved it"
  locally.
- Verify detects edits but not a chain replaced wholesale and re-signed,
  or cut short; the trust model says so.
- Approvals from other people are out of scope until a new decision.

## See also

[Chain](../reference/chain.md), [trust model](../reference/trust-model.md).
