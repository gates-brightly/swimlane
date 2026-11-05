# Manifest

Before a lane waits for anything, `swim run` pins what it will run and
stages a read-only copy under `.swim/runs/<run>/<N>/` (`$SWIM_STAGE`,
`$SWIM_MANIFEST`). The lane runs that copy, so an edit to the working tree
afterwards, even mid-round, changes nothing. Right before the lane starts,
swim checks the copy (and a queued batch's pins) again: a mismatch refuses
or skips the lane.

## Pinned

In `manifest_sha256`, what approvals sign ([format](formats.md#manifest_sha256)):

| | |
|---|---|
| script | The lane's file. |
| includes | `# Includes:` matches and `# Cache:` lockfiles: what the header names, never what the body seems to read. |
| sidecar | `lane.N.yml`. |
| runner | How it runs as the repo says: bash, short form, its `#!` line, a `swim.yml` runner. |
| config | `swim.yml`'s policy keys (`blocked_commands`, `runners`, `approvals`, `signing`, `secret_env*`, `secret_patterns_auto`, `env`) and `lookup.yml`'s `require:`. |

Recorded but not signed, since they differ per machine: the env files'
hash and the runner binary's path and hash. Not pinned: other binaries on
`PATH`, packages, remote state.

The header is the lane's contract: swim reads what a lane may do (its
guards, the files it reads) from the header, never the body, and refuses
at run time whatever the body does beyond it.

## Probes

Facts swim can't pin, declared so a change between approval and use stops
the guarded step as drift:

```bash
# Observe: aws_account = aws sts get-caller-identity --query Account --output text
# Observe: path terraform aws
```

Each is read when a guard is approved and again just before it returns. A
`path` probe reports the binary `PATH` finds and its hash. Probes must only
read. They narrow the gap between approval and use; they don't close it.
