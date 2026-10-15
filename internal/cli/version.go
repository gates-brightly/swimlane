package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/version"
)

// cmdVersion prints swim's version, then the newest release tag in swim's
// repo and how this build stands against it. Reaching the repo is best
// effort: it says why when it can't, and never fails the command.
func cmdVersion(args []string) error {
	if len(args) > 0 {
		return usagef("usage: swim version")
	}
	fmt.Println("swim", version.Long())
	tag, err := version.LatestTag(5 * time.Second)
	fmt.Print(latestText(tag, err))
	return nil
}

// latestText is swim version's report on the newest release tag.
func latestText(tag string, err error) string {
	if err != nil {
		return "latest release: unknown (" + err.Error() + ")\n"
	}
	cmp, ok := version.Compare(tag)
	switch {
	case !ok:
		return fmt.Sprintf("latest release: %s (this build is from %s, between releases)\n", tag, day(version.Date()))
	case cmp > 0:
		return fmt.Sprintf("latest release: %s, newer than this swim\n  update: %s\n  then:   swim changelog --since %s\n",
			tag, strings.TrimSuffix(version.InstallCmd, "@latest")+"@"+tag, version.String())
	case cmp == 0:
		return "latest release: " + tag + " (this one)\n"
	}
	return "latest release: " + tag + " (this build is newer)\n"
}

// day shows a YYYYMMDD date as YYYY-MM-DD.
func day(d string) string {
	if t, err := time.Parse("20060102", d); err == nil {
		return t.Format("2006-01-02")
	}
	return d
}
