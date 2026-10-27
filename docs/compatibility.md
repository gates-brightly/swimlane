# Compatibility

Where swim runs, what it needs, and which parts you can build on.

[← back to the README](../README.md#install-and-versions) ·
[CHANGELOG](../CHANGELOG.md)

## Platforms

| | |
|---|---|
| **Supported** | Linux and macOS. CI runs every change on `ubuntu-24.04` and `macos-15` (named images, so one changing between runs isn't what turns CI red), with macOS's own bash 3.2 first on `PATH`. |
| **Not supported** | Windows. swim relies on `flock` for locks, on process groups (`Setpgid`) for interrupts, and on unix sockets between swim and its lanes. |

## Building from source

Go **1.27.1** or newer: the `go` line in `go.mod`. `go install
github.com/gates-brightly/swimlane/cmd/swim@latest` builds it; with an
older Go, the `go` command fetches the right toolchain when
`GOTOOLCHAIN` allows it.

## What swim needs at run time

| Needed | For |
|---|---|
| **bash 3.2 or newer** | Lane scripts and swim's lane library (`swim lib`). The library and the e2e scenarios stay compatible with macOS's bash 3.2, and `swim lint` warns about bash 4 features in lane scripts (`bash4`). |
| **git** | swim finds the repo's root with git (`swim doctor` checks it). |
| **ssh-keygen, OpenSSH 8.0 or newer** | Signing the chain and approvals, and `swim verify`. Without it, `signing: auto` records unsigned entries; `signing: required` refuses to run. |
A lane in another language calls the plain CLI (`swim run "<label>" --
cmd`, `swim guard FLAG "<action>"`) and needs only its own interpreter.

## Versions

`swim --version` prints `0.<YYYYMMDD>.<index>`: the release's date and its
number that day, tagged `v0.<YYYYMMDD>.<index>` (until 0.20261014.1:
`<breaking>.<YYYYMMDD>`). A build between releases says `0.<YYYYMMDD>`.

**The version says nothing about compatibility.** Syntax versions do.

## Syntax versions and the lock

Three formats have a syntax version (`internal/syntax`):

| Syntax | Covers | Says it |
|---|---|---|
| **script** | Lane scripts | In the file: `# swim: syntax 4` (what `swim new` writes) |
| **log** | Lane logs | In the file's first line: `# swim lane log \| syntax 2 \| swim N` |
| **state** | swim's state files: `.swim/status.yml`, the chain, queued batches, signed approvals | Not in the files: only the lock records it |

`.swim.lock`, committed at the repo root, pins all three. The first run in
a repo writes it.

**A syntax bump** means a release changed one of these formats in a way an
older swim (or an older file) can't handle:

- The release's `CHANGELOG.md` entry has **Upgrading** notes, and
  `swim lock --help` lists what each syntax version changed.
- A swim whose syntaxes differ from the lock refuses to run lanes or write
  new ones, and says whether to update swim or to upgrade the repo
  (`swim plan`, `swim status` and `swim log` still work, with a warning).
- Upgrading a repo: read the notes, update the lane scripts, let running
  lanes finish, then `swim lock --upgrade` and commit `.swim.lock`.
- Older files are migrated automatically where swim can, with the
  originals kept under `.swim/migrations/`. Older lane scripts run as they
  are: a syntax 2 script is never rewritten to 3.

A release that changes no syntax never touches the lock, however many
releases there are in a day. See [recovery](recovery.md#a-lock-mismatch)
when the lock and your swim disagree.

## Stability tiers

| Tier | What | Promise |
|---|---|---|
| **Stable** | The commands and flags in `swim guide` and `swim <command> --help`; the lane script syntax and the lane library; the lane log syntax; exit codes (`0` means everything selected passed; waits exit with the result they waited for). | Changes are additive. A format change bumps its syntax version (scripts, logs), with Upgrading notes. A removal is deprecated first (below). |
| **Versioned by schema** | Every `--yaml` output: `swim.run/v1`, `swim.status/v1`, `swim.log/v1`, `swim.plan/v1`, `swim.history/v1`, `swim.queue-list/v1`, `swim.env/v1`, `swim.lint/v1`, `swim.doctor/v1`, `swim.verify/v1`, `swim.report/v1`, `swim.clean/v1`, `swim.cache/v1`, `swim.ps/v1`. The files swim keeps: `swim.queue/v1` (queued batches), `swim.runrec/v1` (run records), `swim.approval/v1` (signed approvals), `swim.manifest/v1` (a run's staged execution manifests), `swim.earmark/v1` (a reader's place, `swim report --earmark`), `swim.cache-index/v1` (when each tool cache was last used), `swim.cache-snapshot/v1` (a saved installed tree, `# Cache: snap:`). | Each document names its schema. Within a version, fields are only added; removing or renaming one bumps the version. State files are also covered by the state syntax in `.swim.lock`. |
| **Experimental** | The hub: its HTTP API (`/api/enrollments` and `/api/events`, `swim.hub/v2`), the swimmer reports and finished runs it keeps (`--state DIR/runs/`), and its state file (`swim.hub-state/v2`; a hub reads `swim.hub-state/v1` and saves it as v2). Commands starting with `_` (`swim _mark`, `swim _approve`), which lanes use internally, and the approval socket between them and `swim run` ([proto/swim/approval/v1](../proto/swim/approval/v1/README.md)). Go packages under `internal/`. | May change in any release. Changes are noted in the CHANGELOG. |

## Deprecations

A stable feature is removed in steps, each announced in `CHANGELOG.md`:

1. **Deprecated:** the release's **Changed** section says so, with how to
   migrate and when it goes. swim warns where it's used (a `WARN` in the
   log, a `swim doctor` or `swim lint` finding), and `swim doctor --fix`
   migrates it where that's safe.
2. **Removed:** in a later release that bumps a syntax version, with
   **Upgrading** notes, so a repo's lock stops it running on the new swim
   until someone has read them.

`swim changelog --since <version>` prints everything after the version you
came from.
