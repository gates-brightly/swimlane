# swim reference

One page per topic, each the only place that topic is written down. The
binary carries the same pages: `swim guide <topic>` prints one, `swim
guide all` prints them all, and `swim <command> --help` has each command's
detail.

Using swim:

- [concepts](concepts.md): every term, in one table.
- [agents](agents.md): how an AI planner and its workers use swim.
- [lanes](lanes.md): writing a lane script: functions, step options,
  header, stages, checkpoints, lint.
- [other-languages](other-languages.md): lanes in Python, TypeScript, or
  any program.
- [guards](guards.md): how a guard is decided, signed approvals,
  `lookup.yml`, decisions.
- [running](running.md): dependencies, parallelism, locks, the live view,
  interrupts.
- [queue](queue.md): batches, the swimmer, waiting.
- [logs](logs.md): the lane log, `.swim.log`, `status.yml`, YAML.
- [env](env.md): env files.
- [ci](ci.md): `swim ci`, and signing off from your machine.
- [config](config.md): settings, and every file swim keeps.
- [commands](commands.md): every command, one line each.
- [safety](safety.md): the rules every round follows.

How it holds together:

- [manifest](manifest.md): what a round pins and an approval signs.
- [chain](chain.md): the signed record, `swim verify`.
- [architecture](architecture.md): where credentials live and where policy
  is enforced.
- [trust-model](trust-model.md): what swim protects against, what it
  doesn't, and the test behind each claim.
- [state](state.md): every state swim records, and what moves it.
- [formats](formats.md): what swim signs, byte for byte.
- [approval-socket](approval-socket.md): how a lane's swim commands ask
  `swim run` to decide a guard (internal).

When you need it:

- [recovery](recovery.md): when something goes wrong, and the commands
  that fix it.
- [compatibility](compatibility.md): install, platforms, versions, syntax
  versions, what's stable.
- [why](why.md): why swim exists, and how it compares.
