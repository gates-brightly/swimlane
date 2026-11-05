# swim's docs

swim runs AI-written operational work safely. A session (Claude, or a
person) writes a round of work as a small script, one per numbered lane;
the operator runs it with their own access, where the session can't reach;
every step, result and approval lands in a plain-text record the session
reads back to decide the next round. Destructive steps are dry runs until a
person approves them, and the record says who decided what.

## Start here

1. [Your first five minutes](../README.md#your-first-five-minutes): your
   AI writes two lanes, you run them, a guarded step is refused until you
   say yes, and the AI reads the log back. Tested, like the hour below.
2. [quickstart.md](quickstart.md): an hour, from a first round to a guard,
   a signed approval, a dependency, a queue and a swimmer, and reading the
   record. Every command is tested.
3. [usage.md](usage.md): how swim works, the
   [usage patterns](usage.md#common-usage-patterns) people run day to day,
   and the reference for every feature, command, file and setting.

## Concepts

Four ideas are all you need to run swim:

- **Lane**: a piece of work, a numbered script (`lane.N.sh`) your AI writes
  ([usage](usage.md#lanes-rounds-and-jobs)).
- **Round**: one go at that work, run by you, with your access.
- **Guard**: a step that needs your approval, and is a dry run until it has
  it, at the prompt, with a flag or by signature
  ([usage](usage.md#guards-and-approvals)).
- **Log**: what happened, step by step, for the AI to read back
  ([usage](usage.md#logs)).

The rest is built from them, and you can learn it when you need it:

- **Gates and stages**: a gate is a check that stops the round when it
  fails; stages (snapshot, check, change, verify) say where a round is, and
  a read-only preview stops where change begins
  ([usage](usage.md#lane-scripts)).
- **The execution manifest**: what an approval covers, the script and
  everything that decides what it does
  ([usage](usage.md#execution-manifest)).
- **The queue and swimmers**: batches pinned to their scripts, run by `swim
  watch` ([usage](usage.md#queue-watch-and-wait)).
- **The chain of custody**: every round and approval, hash-linked and
  signed ([usage](usage.md#chain-of-custody)).

## Reference

- `swim guide`: the full guide, compiled into the binary, and `swim
  <command> --help` for one command. One section of the guide:
  `swim guide | awk '/^READING RESULTS$/{p=1;print;next} p&&/^[A-Z][A-Z ]+$/{exit} p'`
- [The command list](usage.md#commands), [the files swim
  keeps](usage.md#files) and [config](usage.md#config).
- [compatibility.md](compatibility.md): where swim runs, what it needs, and
  which outputs and files you can build on (each `--yaml` schema).
- [formats.md](formats.md): exactly what an approval signs and a manifest
  hash covers, and when upgrading swim stales one.

## Deep dives

- [architecture.md](architecture.md): how a round gets from a session to a
  signed record, where credentials live and where policy is enforced.
- [trust-model.md](trust-model.md): what swim protects against, what it
  doesn't, and the test behind each claim.
- [state.md](state.md): every state swim records, and what moves it.
- [approval-socket.md](approval-socket.md): how a lane's swim commands ask
  `swim run` to decide a guard (internal, experimental).
- [recovery.md](recovery.md): when something goes wrong, what you see and
  the commands that fix it.

## Demos

[demos/](../demos/README.md): six scripted scenarios, from a guarded change
to tamper evidence, each a script you can run.
