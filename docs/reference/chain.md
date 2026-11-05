# Chain of custody

Every approval and round is recorded in `.swim/chain.jsonl`, one signed
entry per line, each holding the SHA-256 of the line before it.

| Entry | Records |
|---|---|
| `approve` | Lane, job, script and manifest hashes, the guard and its action, the `Observe:` facts, who approved and how: signature, standing, prompt, flag, `-y` or queue. |
| `round` | Script and manifest hashes, the hash of the round's log section, result, exit, the approvals it used, kept checkpoints, cache restores, inputs read. |
| `decide` | A decision: question, choices, the number, who and how. |
| `run` | `swim run`'s attestation when its last lane exits: what each lane ran and every approval it recorded. |
| `grant` / `revoke` | Approvals given or withdrawn on this machine. |
| `repair` | `swim chain repair` removed a half-written last line (size, hash, the `.bak` that keeps it). |

```sh
swim verify                 # in levels, one line each (exit 1 if one says no; --yaml for CI)
swim history [--oneline]    # the chain, newest first, like git log
swim chain repair           # after a crash cut a write short
swim chain checkpoint       # send the signed head to chain_anchors now
swim chain export --to file:/mnt/audit/orders
swim keys [trust KEY --sign | pin]
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

## Verify levels

```
consistent  yes  418 entries, links and signatures match; logs, scripts and approvals as recorded
trusted     yes  2 signers, all in trust.yml (pinned 2026-09-30)
anchored    yes  extends checkpoint seq 412 (2026-10-14T09:12:00Z) from file:/mnt/audit/orders; 6 entries since
authorized  yes  31 approvals, each met its lookup.yml rule
```

| | |
|---|---|
| **consistent** | Links, signatures, logs, kept scripts, staged copies and approvals match what's recorded. Any edit, removal or reordering fails it. |
| **trusted** | Every signer is in `trust.yml` (committed), whose key set is pinned outside the repo (`~/.config/swim/trust-pins.yml` on first use or `swim keys pin`; CI: `--trust-pin`). A change counts only when a trusted key signs it. A chain re-signed with a new key fails here. |
| **anchored** | The chain still holds every checkpoint sent to `chain_anchors` (`file:` paths outside the repo; every run, `swim ci` and watch batch sends one). Truncation or replacement after a checkpoint left the machine fails here. `--against DEST` checks an exported copy. |
| **authorized** | Each approval met `lookup.yml`'s rule for its guard. |

An anchor proves nothing about entries after its last checkpoint, and
whoever holds the runner key and can write every anchor can rewrite
everything: keep anchors out of the operator's reach.

## The store

The chain holds hashes, and the repo can go. The store keeps the bytes in
your profile (`~/.local/share/swim/store`, 0700): every file a lane's
manifest pins, as `swim run` stages it, then the round's log section and
result. Kept once each, never removed, shared by a repo's clones.

```sh
swim store                              # this repo's kept rounds (--all)
swim store show RUN [N] [--log|--script|--file PATH]
swim store verify                       # every object against its hash; every chain round kept
```

`store_dir` in config.yml moves it or turns it off; a repo's `swim.yml`
can't. A store that can't take a round warns, and the round runs.
