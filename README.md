# swim

> **A feature must save time, reduce risk or reduce cognitive load in your actual work.**
>
> That's the bar every command in swim has to clear. What doesn't clear it
> doesn't get built, and what stops clearing it gets cut.

[![test](https://github.com/gates-brightly/swimlane/actions/workflows/test.yml/badge.svg)](https://github.com/gates-brightly/swimlane/actions/workflows/test.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**An operator-in-the-loop runner for ops work. Your AI writes the scripts.
You run them, with your access. The logs carry the results back.**

swim is one binary for an engineer doing infrastructure work with an AI
session: migrations, cutovers, prod diagnostics, anything that has to run
where the AI can't reach. No server, nothing your team has to adopt.

| | |
|---|---|
| **Lane** | A piece of work: a numbered script, `lane.N.sh`, that your AI writes. |
| **Round** | One go at that work, run by you, from your shell, with your access. |
| **Guard** | A step that needs your approval. Until you give it, the step is a dry run. |
| **Log** | What happened, step by step, written for the AI to read back. |

Everything else is built from these four.

## Install

```sh
curl -fsSL https://github.com/gates-brightly/swimlane/releases/latest/download/install.sh | sh
```

Pinning a version, building from source, platforms:
[compatibility](docs/reference/compatibility.md#install).

## Your first five minutes

<!-- quickstart -->
```sh
mkdir swim-try && cd swim-try && git init -q
swim init
touch build.tmp cache.tmp          # something for lane 2 to clean up
```

**Ask your AI for a lane.** Open Claude Code (or any coding agent) in
`swim-try` and paste:

> Run `swim guide` to learn swim. Write lane 1: a round that reports
> what's in this repo and how big it is. Lint it, and don't run it: I will.

(`swim skill` installs a Claude skill that already knows swim.) No AI
handy? This is the kind of lane it writes:

```sh
cat > lane.1.sh <<'EOF'
#!/usr/bin/env swim
# swim: syntax 4
# Round: Report what's in this repo

stage snapshot
snapshot "the files" ls -la "$SWIM_ROOT"

stage check
step "how big it is" du -sh "$SWIM_ROOT"
EOF
```

**Run it,** and read what came back:

```sh
swim run 1
swim log 1
```

Every step is in the log with its command, output, exit code and time. The
AI reads that, not your terminal.

**A guarded step, refused.** Ask your AI: *Write lane 2: delete the .tmp
files here, behind a guard.*

```sh
cat > lane.2.sh <<'EOF'
#!/usr/bin/env swim
# swim: syntax 4
# Round: Delete the .tmp files
# Guards: TRY_ALLOW_DELETE  delete the .tmp files

stage snapshot
snapshot "the .tmp files" bash -c 'ls "$SWIM_ROOT"/*.tmp'

stage change
if guard TRY_ALLOW_DELETE "delete the .tmp files"; then
  step "delete them" bash -c 'rm -v "$SWIM_ROOT"/*.tmp'
fi
EOF
swim run 2 || echo "exit $?: nothing deleted"
ls *.tmp
```

swim asks `Y/N [N]`; press Enter. The step becomes a dry run, and the
files are still there:

```
SKIP  delete the .tmp files (dry run: set TRY_ALLOW_DELETE=1 to approve)
```

**Ask your AI what happened:** *Read `swim log 2`. What would it take to
finish?* It reads the log and tells you `TRY_ALLOW_DELETE=1 swim run 2`.
That's your call:

```sh
TRY_ALLOW_DELETE=1 swim run 2
ls *.tmp 2>/dev/null || echo "the .tmp files are gone"
```
<!-- /quickstart -->

The AI wrote both lanes and explained the result. It never ran anything,
and the delete happened only when you said so. Real work is the same,
except the lane needs your VPN, your SSO session and your prod keys, and
the AI never gets any of them.

## Next

- [The tour](docs/guide.md): gates, guards, approving ahead, dependencies,
  the queue, and the record that proves what ran. Tested, an hour.
- [The reference](docs/reference/README.md): one page per topic. The binary
  carries it: `swim guide`.
- [What swim protects against](docs/reference/trust-model.md), and
  [why it exists](docs/reference/why.md).

MIT licensed. See [LICENSE](LICENSE).
