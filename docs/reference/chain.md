# Chain of custody

Every approval and round is recorded in `.swim/chain.jsonl`, one signed
entry per line, each holding the SHA-256 of the line before it.

| Entry | Records |
|---|---|
| `approve` | Lane, round id, script and manifest hashes, the guard and its action, who approved and how: `prompt`, `preapproved` (the live view), `flag` or `queue`. |
| `round` | Script and manifest hashes, the hash of the round's log section, result, exit, the approvals it used. |
| `run` | `swim run`'s attestation when its last lane exits: what each lane ran and every approval it recorded. |
| `amend` | `swim verify --force` acknowledged gaps or deletions. |
| `repair` | `swim chain repair` removed a half-written last line (size, hash, the `.bak` that keeps it). |

An older swim's entries (`decide`, `grant`, `revoke`, and approve methods
like `auto` or `signature`) are still verified by their own signatures.

```sh
swim verify                 # VERIFIED or NOT VERIFIED (exit 1; --yaml for CI)
swim chain repair           # after a crash cut a write short
```

## Signing

The runner signs with its SSH key (`signing_key`, default
`~/.ssh/id_ed25519`; a `.pub` path signs through ssh-agent; `signing: auto
| required | off`), with `ssh-keygen -Y`, so anyone can check a signature
without swim. Under `swim run`, the runner key vouches once for a run key
kept in swim's memory, which signs the run's entries; a lane is given
nothing that signs. Keep the runner key in ssh-agent with `ssh-add -c` or
on a security key, so a lane can't use it unasked ([the runner
key](trust-model.md#the-runner-key)). If it won't vouch, `required` stops
the run and `auto` records it unsigned.

Each entry is synced as written. A write cut short leaves a last line with
no newline; swim refuses to append until `swim chain repair`. Scripts that
ran are kept by hash in `.swim/scripts/`, pinned copies in `.swim/runs/`.

## Verify

```
VERIFIED: 418 entries, every one signed and linked; logs and scripts as recorded
```

Every link and signature, and every round's log, kept script and pinned
copy, must match what's recorded: any edit, removal or reordering is
BROKEN. Unsigned entries after the last signed one fail as UNSIGNED unless
`--allow-unsigned`; `--strict` also fails on warnings.

A chain replaced wholesale and re-signed with another key, or cut short
at its end, is still consistent: the chain proves what happened to whoever
trusts the machine that keeps it, not to an outside auditor.
