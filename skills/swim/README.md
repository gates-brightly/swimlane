# The swim skill for Claude

A Claude skill that teaches Claude how to work with swim: read the guide
from the installed binary, write lane scripts, hand rounds to the operator,
and read the results back. It never runs or approves anything itself.

- [`SKILL.md`](SKILL.md): when the skill applies, Claude's role, what it may
  run and what it hands over, the loop, the lane rules, and how to talk
  about safety.
- [`reference/lane-recipe.md`](reference/lane-recipe.md): an annotated lane
  (it passes `swim lint`) and patterns.
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

**Version:** written for swim 0.20261028.1 or newer. The skill checks
`swim --version` and tells the user when the binary is older, or when a
command it names is unknown. To update, run `swim skill`: it points at the
copy that matches the installed swim (its release tag). `swim version`
says whether a newer swim is out.

The skill defers to `swim guide` from the installed binary for detail, so
it stays correct as swim changes. Keep it to what the guide doesn't say:
Claude's role and boundaries.

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
`swim report --earmark` and `swim status`.

## Changing it

- A rule that holds for every agent belongs in `internal/help/guide.txt`
  (`swim guide`), not here.
- Raise the version in SKILL.md section 0 when the skill starts relying on
  a newer command or behaviour.
- Check every command and header the skill names against the binary. Lint
  the recipe's lane in a scratch repo after editing it:

```sh
sed -n '/^```bash$/,/^```$/p' reference/lane-recipe.md | sed '1d;$d' > lane.2.sh
swim new 1 setup && swim lint 2
```
