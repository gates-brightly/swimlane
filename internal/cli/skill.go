package cli

import (
	"fmt"

	"github.com/gates-brightly/swimlane/internal/version"
)

// cmdSkill prints how to install the Claude skill for swim (skills/swim in
// swim's repo): this release's copy, else main's.
func cmdSkill(args []string) error {
	if len(args) > 0 {
		return usagef("usage: swim skill")
	}
	fmt.Print(skillText(skillURL()))
	return nil
}

// skillURL is the skill's folder on the web, at this binary's release tag
// (so the skill matches this swim), or main for a build between releases.
func skillURL() string {
	ref := version.ReleaseTag()
	if ref == "" {
		ref = "main"
	}
	return version.RepoWeb + "/tree/" + ref + "/skills/swim"
}

func skillText(url string) string {
	return `The swim skill teaches Claude its side of swim: read swim --help,
write and lint lane scripts, hand runs and approvals to the operator, and read
the results back. It never runs or approves anything itself.

  ` + url + `

Install it: in a Claude Code session, paste

  Install the Claude skill at ` + url + `

Claude puts it in ~/.claude/skills/swim (every repo), or in this repo's
.claude/skills/swim if you ask (teammates get it with the repo). Or copy
the folder there yourself. Start a new session to load it; it begins with
swim report --earmark and swim status.
`
}
