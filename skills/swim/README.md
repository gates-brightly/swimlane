# The swim skill for Claude

Teaches Claude when swim applies and sends it to `swim guide`, the
reference the installed binary carries, so it stays correct as swim
changes. Everything an agent does is in `swim guide agents`
([docs/reference/agents.md](../../docs/reference/agents.md)); a rule that
holds for every agent goes there, not here.

## Install

`swim skill` prints the skill's URL (at the installed swim's release tag)
and a line to paste into Claude Code. Or copy it:

```sh
mkdir -p .claude/skills && cp -R /path/to/swimlane/skills/swim .claude/skills/swim     # one repo
mkdir -p ~/.claude/skills && cp -R /path/to/swimlane/skills/swim ~/.claude/skills/swim # every repo
```

Check it loads: ask "what's the state of the swim lanes?" Claude should
start with `swim status` and `swim queue`, not a full report.
