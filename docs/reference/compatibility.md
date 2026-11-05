# Compatibility

Where swim runs, what it needs, and which parts you can build on.

## Platforms

| | |
|---|---|
| **Supported** | Linux and macOS. CI runs every change on `ubuntu-24.04` and `macos-15` (named images, so one changing between runs isn't what turns CI red), with macOS's own bash 3.2 first on `PATH`. |
| **Not supported** | Windows. swim relies on `flock` for locks, on process groups (`Setpgid`) for interrupts, and on unix sockets between swim and its lanes. |

## Install

```sh
curl -fsSL https://github.com/gates-brightly/swimlane/releases/latest/download/install.sh | sh
SWIM_VERSION=v0.20261102.1 SWIM_INSTALL_DIR=/usr/local/bin sh install.sh   # pin a release, pick where
go install github.com/gates-brightly/swimlane/cmd/swim@latest              # or build it (Go 1.27.1+)
swim version                                                               # is a newer release out?
```

Each release ships static binaries for Linux and macOS on amd64 and arm64
(`swim_<os>_<arch>.tar.gz`, checked by `checksums.txt`) with `install.sh`.
The names carry no version, so `releases/latest/download/<name>` is always
the newest. The installer picks this machine's, checks it, and puts it in
`/usr/local/bin` (if writable) or `~/.local/bin`. In a container, install
at build time.

Building from source needs Go **1.27.1** or newer (`go.mod`'s `go` line);
an older `go` fetches it when `GOTOOLCHAIN` allows.

## What swim needs at run time

| Needed | For |
|---|---|
| **bash 3.2 or newer** | Lane scripts and swim's lane library. The library and the e2e scenarios stay compatible with macOS's bash 3.2, and `swim lint` warns about bash 4 features in lane scripts (`bash4`), and GNU-only tool forms (`gnu-only`): `sed -i` with no suffix, `grep -P`, `date -d`, `stat -c`, `find -printf`, `timeout`. |
| **git** | swim finds the repo's root with git (`swim doctor` checks it). |
| **ssh-keygen, OpenSSH 8.0 or newer** | Signing the chain and signoffs, and `swim verify`. Without it, `signing: auto` records unsigned entries; `signing: required` refuses to run. |
A lane in another language calls the plain CLI (`swim step "<label>" --
cmd`, `swim guard FLAG "<action>"`) and needs only its own interpreter.

## Versions

`swim --version` prints `0.<YYYYMMDD>.<index>`: the release's date and its
number that day, tagged `v0.<YYYYMMDD>.<index>` (until 0.20261014.1:
`<breaking>.<YYYYMMDD>`). A build between releases says `0.<YYYYMMDD>`.

**The version says nothing about compatibility.** Syntax versions do.
This scheme stays: no more changes to how versions are numbered.

## The syntax version

**Script syntax 4 is frozen** (2026-11-03). Lane scripts are what people
write against, so their format stays put while the CLI keeps improving:

- New headers, library functions and forms are additive: a syntax 4
  script that runs today runs on every later swim. The short form
  (`#!/usr/bin/env swim`, the library loaded for you) is one: an
  explicit-form script is unchanged by it, and so is its manifest.
- There is no syntax 5 without a written migration: what changes, why it
  couldn't be additive, and Upgrading notes, in the CHANGELOG and here,
  before the release that ships it.

swim has one syntax version (`syntax.Version` in `internal/syntax`), and
two kinds of file say it:

| File | Says it |
|---|---|
| Lane scripts | Near the top: `# swim: syntax 4` (what `swim new` writes) |
| Lane logs | The first line: `# swim log \| syntax 4 \| lane N` |

swim reads every older file as it is: scripts that say syntax 2 or 3, and
logs that start `# swim lane log | syntax 2`, as written before the one
number. Nothing is migrated or rewritten. A file **newer** than this swim
reads stops it, with how to update swim. A lane script with no syntax line
is a lint error that keeps only that lane from running.

**A syntax bump** means a release changed a format in a way an older swim
can't read. The release's `CHANGELOG.md` entry has **Upgrading** notes.

## Stability tiers

| Tier | What | Promise |
|---|---|---|
| **Stable** | The commands and flags in `swim guide` and `swim <command> --help`; the lane script syntax and the lane library; the lane log syntax; exit codes (`0` means everything selected passed; waits exit with the result they waited for). | Changes are additive. A format change bumps the syntax version, with Upgrading notes. A removal is deprecated first (below). |
| **Versioned by schema** | Every `--yaml` output: `swim.run/v1`, `swim.status/v1`, `swim.log/v1`, `swim.queue-list/v1`, `swim.env/v1`, `swim.lint/v1`, `swim.doctor/v1`, `swim.verify/v1`. The files swim keeps: `swim.queue/v1` (queued batches) and `swim.manifest/v1` (a run's staged execution manifests). | Each document names its schema. Within a version, fields are only added; removing or renaming one bumps the version. |
| **Experimental** | Commands starting with `_` (`swim _mark`, `swim _approve`), which lanes use internally, and the approval socket between them and `swim run` ([approval-socket.md](approval-socket.md)). Go packages under `internal/`. | May change in any release. Changes are noted in the CHANGELOG. |

## Deprecations

A stable feature is removed in steps, each announced in `CHANGELOG.md`:

1. **Deprecated:** the release's **Changed** section says so, with how to
   migrate and when it goes. swim warns where it's used (a `WARN` in the
   log, a `swim doctor` or `swim lint` finding), and `swim doctor --fix`
   migrates it where that's safe.
2. **Removed:** in a later release, with **Upgrading** notes.

`swim changelog --since <version>` prints everything after the version you
came from.
