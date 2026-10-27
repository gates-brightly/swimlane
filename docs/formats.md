# Frozen formats

What swim signs and pins, byte for byte. Approvals, queued
batches and hub commands all record these hashes, so changing
how one is computed would void every one of them. **Upgrading swim
doesn't stale an approval or a pin** while these formats hold. Only
changes to what you pinned do: your script, the files it includes, its
sidecar, how it runs, the `swim.yml` policy keys you set, or
`lookup.yml`'s `require:` rules.

[← back to the docs](README.md) · [trust model](trust-model.md)

## The approval statement

Every event in an `approvals/lane.N.<sha12>[.m<man8>]….signature` file is
an SSH signature over this text and the event's own type, previous event,
signer and time (`internal/sigfile`, `Statement`):

```
swim approval v1
script: lane.3.sh
script-sha256: <sha256 of the script with its # Includes: files>
lane: 3
job: <job id>
round: <round goal>
cells: <cell;cell>            (only in statements older swims wrote for a matrix cell)
evidence-sha256: <sha256>     (only when approved with evidence)
manifest-sha256: <manifest_sha256>   (only when bound to a manifest)
approves: <FLAG,FLAG | all>
expires: <time | never>
```

Optional lines appear only when they apply, so statements from before
each one existed read the same.

## manifest_sha256

The SHA-256 of this text (`internal/manifest`, `Pinned.Hash`):

```
swim manifest v2
lane: <N>
script: <path> <sha256>
include: <path> <sha256>                         (one per pinned file, sorted by path)
sidecar: <path> <sha256>                         (when lane.N.yml exists)
module: <name> <version> <source> <kind> <sha256>   (only in manifests an older swim wrote for a lane with # Modules:)
runner: <how it runs: bash, #!..., swim.yml ..., direct, local .ext>
config: <policy hash>
lookup-rules: <sha256 of lookup.yml's require: rules>
```

**The policy hash** is the SHA-256 of `swim policy v2\n` followed by one
line per policy key the committed `swim.yml` sets, sorted by key:
`<key> <sha256 of the YAML "key: value">`. A key that `swim.yml` doesn't
set adds nothing. A list or map that it writes, even an empty one, counts
as set. The policy keys are `config.PolicyKeys`. A key that isn't policy,
such as `max_parallel` or `interrupt`, never counts. Neither does the
operator's own `config.yml`, which differs from machine to machine.

So **a policy key that swim adds later changes nothing** for a repo that
doesn't set it. The key goes into `PolicyKeys` and nothing else changes.

Left out on purpose, because they differ per machine: the env files'
values, and the runner binary's path and hash (an OS update would
otherwise stale every approval). These are recorded under `local:` in the
staged manifest and checked before exec.

## Task names

A `# Task: renew-certs` header names a lane. The name is an alias: the
lane's number is still what approval statements, manifests, the chain,
status and the hub record. Renaming a task changes the script's bytes,
like any edit. Where swim takes a lane, it reads its argument in this
order:
1. With a `--` among the arguments (`swim run "<label>" -- cmd`, inside a
   lane), it's a step, and nothing is resolved.
2. All digits: a lane number.
3. A task name, matched exactly.
4. A job id, or a prefix of 8 or more characters.

`# After:` reads a task name the same way.

`swim lint` keeps these unambiguous. A task name may not be:
- a swim command;
- only hex digits and `-` (that could be a job id's prefix);
- another lane's name.

## Changing a format

Don't change one in place. Golden tests pin each hash
(`internal/manifest/format_test.go`, `internal/config/policy_test.go`).
If a format must change once there are approvals to keep:
1. Keep the old hash function, frozen.
2. Record both hashes.
3. Make every comparison accept either (`Manifest.Matches`,
   `queue.Lane.PinsManifest`).
4. Say so in the CHANGELOG.
