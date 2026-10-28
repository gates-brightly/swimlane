# swim

[![test](https://github.com/gates-brightly/swimlane/actions/workflows/test.yml/badge.svg)](https://github.com/gates-brightly/swimlane/actions/workflows/test.yml)
[![license: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Your AI writes the ops change. You approve the exact script swim will
run. swim runs that copy and keeps a signed, hash-linked record of it.**

swim is a single-binary runner for small, guarded ops scripts (*lanes*).
An AI session writes them; you run them with your own access, where the AI
can't reach. Every destructive step is a dry run until a person approves
it, and an approval binds the exact bytes: the script and every file it
pins. `swim verify` checks the record: every entry signed, linked to the
one before, and still matching what's on disk.

**What it doesn't do:** a lane runs as you, so it can do anything you can,
including use your signing key wherever you can without being asked.
Approvals and the record show what an honest lane did and who approved it;
they don't confine a hostile script. Review what you approve. See
[the trust model](docs/trust-model.md).

## The 90-second tamper story

```sh
# The AI wrote lane.1.sh: one guarded step that runs install.sh, which
# its header pins (# Includes: install.sh).
swim approve 1 --guards CERT_ALLOW_ROTATE    # you sign the script, install.sh, how it runs and the policy

echo 'echo DROP EVERYTHING' > install.sh     # anyone changes a file it uses after you signed
swim run 1 --no-prompt
#   SKIP  replace the live cert (dry run: ...signature approves another version of lane.1.sh
#         (approved c4d0cc7ede49, running 12a561d369b4); review the change ..., then:
#         swim approve 1 --guards CERT_ALLOW_ROTATE)

git checkout install.sh && swim run 1         # back to the bytes you signed
#   APPROVED  replace the live cert (approved by me (signed))
#   PASS  install the new cert

sed -i 's/new cert/old cert/' .swim/logs/agent1.log   # someone edits the record afterwards
swim verify
#   BROKEN: entry #5: swim 1's log for run r-20261023T224837Z-a11f was changed after the round ended
#   NOT VERIFIED: consistent: 1 problems (BROKEN, above)
```

A changed script, or a file it pins, falls back to a dry run. A file it
uses but doesn't pin isn't there when it runs: the lane runs in a staged
copy of what it pins (`# Workspace: live` runs it in the working tree, and
the approval says so). A changed record fails `swim verify`.

## Install

swim is one Go binary (Go 1.27.1 or newer to build), for Linux and macOS:

```sh
go install github.com/gates-brightly/swimlane/cmd/swim@latest
export PATH="$(go env GOPATH)/bin:$PATH"      # if ~/go/bin isn't on your PATH yet
swim --version                                # e.g. swim 0.20261014.1
```

Pinning a version, building from a checkout, and what each release
promises: [install and versions](docs/usage.md#install-and-versions).

## Quickstart

Five minutes, no cloud credentials: two harmless lanes in a scratch repo.
Paste each block into a shell.

<!-- quickstart -->
```sh
mkdir swim-quickstart && cd swim-quickstart && git init -q
swim init                         # config entry, .gitignore block, .swim/status.yml
swim new 1 "Say hello"            # writes lane.1.sh from a template

# Replace the template with one harmless step:
cat > lane.1.sh <<'EOF'
#!/usr/bin/env bash
# swim: syntax 4
# Round: Say hello
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1

stage check
run "say hello" echo "hello from swim"

summary
EOF

swim lint 1                       # check the script before you run it
swim run 1 --plain                # run it
swim log 1                        # the round, step by step
swim verify --allow-unsigned      # check the chain of custody (drop the flag once you sign: ~/.ssh/id_ed25519)
```

A destructive step goes behind a guard, and is a dry run until a person
approves it:

```sh
swim new 2 "Write hello.txt"
cat > lane.2.sh <<'EOF'
#!/usr/bin/env bash
# swim: syntax 4
# Round: Write hello.txt
# Guards: HELLO_ALLOW_WRITE  write hello.txt in the repo (quickstart)
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 2

stage snapshot
snapshot "what's there" ls

stage change
if guard HELLO_ALLOW_WRITE "write hello.txt (quickstart)"; then
  run "write hello.txt" bash -c 'echo hello > "$SWIM_ROOT/hello.txt"'
fi

summary
EOF

swim run 2 --plain --no-prompt || echo "exit $?: a dry run"   # SKIP: nothing written
HELLO_ALLOW_WRITE=1 swim run 2 --plain     # approved up front: the step runs
cat hello.txt
swim history --oneline                     # the chain: rounds and the flag's approval, newest first
```
<!-- /quickstart -->

Without `--no-prompt` on a terminal, swim asks you instead: `swim 2
requires human approval of task: write hello.txt (quickstart) Y/N [N]`.
For the next hour (signed approvals, dependencies, a queue and a
swimmer), see [swim in an hour](docs/quickstart.md).

## What swim protects against

Approval, signing and the chain prove **who decided what**. They don't make
a script safe: an approved script does whatever it says, with the access of
whoever runs it.

- **The AI never holds your access.** It writes lanes; only you, or a
  swimmer you control, runs them, with your credentials.
- **The approved bytes run.** An approval binds the lane's execution
  manifest (script, the files it pins, how it runs, the policy), and the
  lane runs a staged, read-only copy of exactly that. Change any of it and
  the step is a dry run again.
- **A lane can't approve itself.** Under `swim run` the launcher decides
  every guard, records each approval itself, and signs the run's entries
  with a key that lives only in its memory.
- **Bypasses are recorded.** `FLAG=1`, `-y` and `swim watch --approved`
  skip the question, and each leaves its mark in the log, `swim status`,
  `swim history` and the chain.
- **A chain you can believe.** `swim verify` says how far, in levels:
  consistent (links and signatures), trusted (signers pinned outside the
  repo), anchored (checkpoints sent elsewhere) and authorized (each approval
  met `lookup.yml`'s rule).
- **Out of scope:** a malicious or mistaken script that is approved and run
  with broad credentials. Guards and lint are conventions, not a sandbox:
  use scoped credentials and OS isolation, and keep your signing key in
  `ssh-agent` with `ssh-add -c` or on a security key.

The details, and the test behind each claim, are in the
[trust model](docs/trust-model.md).

## Where next

- [Using swim](docs/usage.md): how it works, common usage patterns, and
  the reference for every feature, command, file and setting.
- [swim in an hour](docs/quickstart.md): a tested walk from a first round
  to guards, signed approvals, a dependency, a queue and a swimmer.
- [The docs](docs/README.md), in reading order: architecture, state
  machines, recovery, compatibility, frozen formats.
- [The trust model](docs/trust-model.md): what swim protects against, what
  it doesn't, and the test behind each claim.
- **AI agents:** run `swim guide`, the full guide for planners and
  workers, or `swim skill` to install the Claude skill.
- [The product brief](swimlane.md): why swim exists and what it must
  guarantee.

## License

MIT. See [LICENSE](LICENSE).
