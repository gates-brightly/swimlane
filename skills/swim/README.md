# The swim skill for Claude

A Claude skill that teaches Claude how to work with swim: read the guide
from the installed binary, write lane scripts, hand rounds to the operator,
and read the results back. It never runs or approves anything itself.

- [`SKILL.md`](SKILL.md): what a session needs up front: when the skill
  applies, what Claude runs freely, what it hands over, what it never
  does, the loop, and which guide section to read for the rest.
- [`reference/lane-rules.md`](reference/lane-rules.md): the rules for
  writing a lane, and how to talk about safety.
- [`reference/lane-recipe.md`](reference/lane-recipe.md): an annotated lane
  (it passes `swim lint`) and patterns.
- [`reference/campaigns.md`](reference/campaigns.md): rounds that build on
  each other.
- [`reference/handoff.md`](reference/handoff.md): how to hand a round to the
  operator: run now, or queue.
- [`reference/workers.md`](reference/workers.md): a planner and its workers,
  or two sessions, in one repo without collisions.
- [`reference/streams.md`](reference/streams.md): working several tasks
  at once, dispatching each one's validation and picking it back up when
  its results are in.
- [`reference/triage.md`](reference/triage.md): first-line commands when
  something looks wrong, and what to do after a round fails partway through
  a change.

**Version:** written for a swim whose `swim new` writes the short form
(`#!/usr/bin/env swim`). With an older swim, the skill follows the
template `swim new` writes and tells the user once; it also says when a
command it names is unknown. To update, run `swim skill`: it points at the
copy that matches the installed swim (its release tag). `swim version`
says whether a newer swim is out.

The skill defers to `swim guide` from the installed binary for detail, so
it stays correct as swim changes. Keep SKILL.md to what a session needs
before its first command: Claude's role and boundaries, and the loop.
Detail goes in the guide, or in a reference file SKILL.md points to.

## Install

`swim skill` prints the skill's URL (at the installed swim's release tag)
and a line to paste into Claude Code: "Install the Claude skill at <url>".
Claude does the rest. Or copy it yourself:

For one repo that uses swim (teammates get it with the repo):

```sh
mkdir -p .claude/skills
cp -R /path/to/swimlane/skills/swim .claude/skills/swim
git add .claude/skills/swim
```

For every repo on your machine:

```sh
mkdir -p ~/.claude/skills
cp -R /path/to/swimlane/skills/swim ~/.claude/skills/swim
```

Claude Code picks it up in the next session. Ask "what's the state of the
swim lanes?" to check that it loads: Claude should start with
`swim status` and `swim queue`, not a full report.

## Changing it

- A rule that holds for every agent belongs in `internal/help/guide.txt`
  (`swim guide`), not here.
- Raise the version in SKILL.md's "Is swim here?" when the skill starts relying on
  a newer command or behaviour.
- Check every command and header the skill names against the binary. Lint
  the recipe's lane in a scratch repo after editing it:

```sh
sed -n '/^```bash$/,/^```$/p' reference/lane-recipe.md | sed '1d;$d' > lane.2.sh
swim new 1 setup && swim lint 2
```
