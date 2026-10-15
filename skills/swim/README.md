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
  operator: run now, queue, signed approvals.

The skill defers to `swim --help` from the installed binary for detail, so
it stays correct as swim changes. Keep it to what the guide doesn't say:
Claude's role and boundaries.

## Install

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
  (`swim --help`), not here.
- Check every command and header the skill names against the binary. Lint
  the recipe's lane in a scratch repo after editing it:

```sh
sed -n '/^```bash$/,/^```$/p' reference/lane-recipe.md | sed '1d;$d' > lane.2.sh
swim new 1 setup && swim lint 2
```
