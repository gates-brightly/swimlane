# Frozen formats

What swim pins, byte for byte. The chain and queued batches record these
hashes, so changing how one is computed would void every one of them.
**Upgrading swim doesn't stale a pin** while these formats hold. Only
changes to what you pinned do: your script, the files it includes, its
sidecar, or how it runs.

## manifest_sha256

The SHA-256 of this text (`internal/manifest`, `Pinned.Hash`):

```
swim manifest v2
lane: <N>
script: <path> <sha256>
include: <path> <sha256>                         (one per pinned file, sorted by path)
sidecar: <path> <sha256>                         (when lane.N.yml exists)
module: <name> <version> <source> <kind> <sha256>   (only in manifests an older swim wrote for a lane with # Modules:)
runner: <how it runs: bash, #!..., direct, local .ext; swim.yml ... in older manifests>
config: <policy hash>
lookup-rules:                                    (empty: once lookup.yml's require: rules, which swim no longer reads)
```

**The policy hash** is now always `SHA-256("swim policy v2\n")`, the hash
of no policy keys. (An older swim added a line per policy key a repo's
`swim.yml` set; swim no longer reads one, so a lane's `manifest_sha256` is
what it was for a repo that set none.) The operator's `config.yml` never
counts.

Left out on purpose, because they differ per machine: the env files'
values, and the runner binary's path and hash (an OS update would
otherwise stale every pin). These are recorded under `local:` in the
staged manifest and checked before exec.

## Task names

A `# Task: renew-certs` header names a lane. The name is an alias: the
lane's number is still what manifests, the chain, and status record. Renaming a task changes the script's bytes,
like any edit. Where swim takes a lane, it reads its argument in this
order:
1. All digits: a lane number.
2. A task name, matched exactly.
3. A round id, or a prefix of 8 or more characters.

`# After:` reads a task name the same way.

`swim lint` keeps these unambiguous. A task name may not be:
- a swim command;
- only hex digits and `-` (that could be a round id's prefix);
- another lane's name.

## Changing a format

Don't change one in place. Golden tests pin each hash
(`internal/manifest/format_test.go`, `internal/config/policy_test.go`).
If a format must change once there are pins to keep:
1. Keep the old hash function, frozen.
2. Record both hashes.
3. Make every comparison accept either (`Manifest.Matches`,
   `queue.Lane.PinsManifest`).
4. Say so in the CHANGELOG.
