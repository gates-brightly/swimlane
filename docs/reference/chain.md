# Chain of custody

Every approval and round is recorded in `.swim/chain.jsonl`, one signed
entry per line, each holding the SHA-256 of the line before it.

| Entry | Records |
|---|---|
| `approve` | Lane, job, script and manifest hashes, the guard and its action (older entries: the `Observe:` facts), who approved and how: prompt, preapproved (the live view), flag or queue (older entries: auto (`-y`), signature, standing, hub-signed, hub-button). |
| `round` | Script and manifest hashes, the hash of the round's log section, result, exit, the approvals it used (older entries: the outputs it made and inputs it read). |
| `decide` | An older swim's decision (`decide`, which swim no longer has): question, choices, the number, who and how. Still verified. |
| `run` | `swim run`'s attestation when its last lane exits: what each lane ran and every approval it recorded. |
| `grant` / `revoke` | Signed approvals an older swim gave or withdrew. |
| `repair` | `swim chain repair` removed a half-written last line (size, hash, the `.bak` that keeps it). |

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
ran are kept by hash in `.swim/scripts/`, staged copies in `.swim/runs/`.

## Verify

```
VERIFIED: 418 entries, every one signed and linked; logs and scripts as recorded
```

Every link and signature, and every round's log, kept script, staged copy
and inputs, must match what's recorded: any edit, removal or reordering is
BROKEN. Unsigned entries after the last signed one fail as UNSIGNED unless
`--allow-unsigned`; `--strict` also fails on warnings.

A chain replaced wholesale and re-signed with another key, or cut short
at its end, is still consistent: the chain proves what happened to whoever
trusts the machine that keeps it, not to an outside auditor.
