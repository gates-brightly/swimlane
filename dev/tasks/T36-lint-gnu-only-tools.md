# T36. Lint warns on GNU-only tool forms (P2, gap)

Source: agents in a Linux sandbox try a command there, then write it into
a lane a macOS operator runs, where the BSD tool rejects it or does
something else. The skill's lane rules now list the common ones, but a
list in prose is easy to miss.

**Today:** the `bash4` check (internal/lint/checks.go:506, registered at
internal/lint/lint.go:104) warns on bash 4 forms macOS bash 3.2 can't
run. Nothing checks the tools a lane calls.

**Do:** a `gnu-only` warning, in the style of `bash4`, for forms a
stock Mac doesn't run as written:
- `sed -i` with no suffix (`sed -i.bak` works on both)
- `date -d`, `grep -P`, `stat -c`, `find ... -printf`
- `timeout` (point at the step's `--timeout`)

Each finding names the portable form in its fix. Warn, not error: a repo
whose operators run Linux, or have GNU tools on PATH, is fine.

**Done when:** `TestEachCheck` has a row per form, and the lint corpus
shows the portable forms stay quiet.
