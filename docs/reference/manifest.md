# Manifest

Before a lane waits for anything, `swim run` pins what it will run and
stages a read-only copy under `.swim/runs/<run>/<N>/` (`$SWIM_STAGE`),
with its manifest beside it in `.swim/runs/<run>/<N>.manifest.yml`
(`$SWIM_MANIFEST`), so the lane's directory holds only its pinned files.
The lane runs that copy, so an edit to the working tree
afterwards, even mid-round, changes nothing. Right before the lane starts,
swim checks the copy (and a queued batch's pins) again: a mismatch refuses
or skips the lane.

## Pinned

In `manifest_sha256`, what the chain and a queued batch pin ([format](formats.md#manifest_sha256)):

| | |
|---|---|
| script | The lane's file. |
| includes | `# Includes:` matches: what the header names, never what the body seems to read. |
| sidecar | `lane.N.yml`. |
| runner | How it runs as the repo says: bash, short form, its `#!` line; `local .ext` for a runner from your config. |
| config | The policy hash of no keys: once a committed `swim.yml`'s policy keys ([format](formats.md#manifest_sha256)). |

Recorded but not signed, since they differ per machine: the env files'
hash and the runner binary's path and hash. Not pinned: other binaries on
`PATH`, packages, remote state.

The header is the lane's contract: swim reads what a lane may do (its
guards, the files it reads) from the header, never the body, and refuses
at run time whatever the body does beyond it.
