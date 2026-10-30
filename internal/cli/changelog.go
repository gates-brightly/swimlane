package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane"
	"github.com/gates-brightly/swimlane/internal/changelog"
	"github.com/gates-brightly/swimlane/internal/ui"
	"github.com/gates-brightly/swimlane/internal/version"
)

// cmdChangelog prints revisions from the changelog built into this binary.
// It needs no repo, so it works anywhere right after an update.
func cmdChangelog(args []string) error {
	var all bool
	var n, since string
	// -n is the one short flag; the shared parser only knows --long ones.
	var long []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-n":
			if i+1 >= len(args) {
				return usagef("-n needs a number of revisions")
			}
			i++
			n = args[i]
		case strings.HasPrefix(a, "-n=") || (strings.HasPrefix(a, "-n") && len(a) > 2 && !strings.HasPrefix(a, "--")):
			n = strings.TrimPrefix(strings.TrimPrefix(a, "-n"), "=")
		default:
			long = append(long, a)
		}
	}
	rest, err := flags{bools: map[string]*bool{"all": &all}, strs: map[string]*string{"n": &n, "since": &since}}.parse(long)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim changelog [-n N] [--since VERSION] [--all]")
	}
	if all && (n != "" || since != "") || n != "" && since != "" {
		return usagef("use one of -n, --since or --all")
	}

	entries := changelog.NonEmpty(changelog.Parse(swimlane.Changelog))
	if !all && n == "" && since == "" {
		if commits := changelog.Commits(version.Recent); len(commits) > 0 {
			printCommits(commits, entries)
			return nil
		}
	}
	if len(entries) == 0 {
		return fmt.Errorf("this build has no changelog")
	}
	total := len(entries)
	switch {
	case all:
	case since != "":
		if entries, err = changelog.Since(entries, since); err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Printf("nothing newer than %s in this swim's changelog\n", changelog.Normalize(since))
			return nil
		}
	default:
		count := 1
		if n != "" {
			if count, err = strconv.Atoi(n); err != nil || count < 1 {
				return usagef("-n needs a positive number of revisions, got %q", n)
			}
		}
		entries = entries[:min(count, len(entries))]
	}

	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	for i, e := range entries {
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(p.Paint(ui.Bold, "## "+e.Heading))
		fmt.Println()
		fmt.Println(e.Body)
	}
	if older := total - len(entries); older > 0 && since == "" && !all {
		fmt.Printf("\n%s\n", p.Paint(ui.Dim, fmt.Sprintf("%d older revision(s): swim changelog -n %d, or --all", older, total)))
	}
	return nil
}

// printCommits is swim changelog's default on a build the justfile
// stamped: the commits it was built from, newest first, each with what its
// message says it's about; then where the release notes are.
func printCommits(commits []changelog.Commit, entries []changelog.Entry) {
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	fmt.Println(p.Paint(ui.Bold, fmt.Sprintf("swim %s: the last %d commits", version.Long(), len(commits))))
	for _, c := range commits {
		fmt.Println()
		fmt.Printf("%s  %s  %s\n", p.Paint(ui.Yellow, c.Hash), p.Paint(ui.Dim, c.Date), p.Paint(ui.Bold, c.Subject))
		if len(c.Areas) > 0 {
			const most = 6
			var as []string
			for i, a := range c.Areas {
				if i == most {
					as = append(as, fmt.Sprintf("+%d more", len(c.Areas)-most))
					break
				}
				as = append(as, fmt.Sprintf("%s (%d)", a.Area, a.Files))
			}
			fmt.Println("    " + p.Paint(ui.Cyan, "changed: "+strings.Join(as, ", ")))
		}
		if c.Summary != "" {
			for _, l := range strings.Split(c.Summary, "\n") {
				fmt.Println("    " + l)
			}
		}
	}
	notes := "swim changelog -n 1 for the release notes, --all for every revision"
	if len(entries) > 0 {
		notes = fmt.Sprintf("swim changelog -n 1 for the release notes (%s), --all for every revision", entries[0].ID)
	}
	fmt.Printf("\n%s\n", p.Paint(ui.Dim, notes))
}
