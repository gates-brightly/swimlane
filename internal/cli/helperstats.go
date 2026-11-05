package cli

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/logparse"
)

// Thresholds for a hand-written check to be a candidate for a helper: run
// in this many rounds, of this many different round goals (a lane rerun
// repeats its own checks; the same check in different work is a pattern).
const (
	candidateRounds = 3
	candidateGoals  = 2
)

// helperUse is one helper function's record in the lane logs.
type helperUse struct {
	name         string
	calls, fails int
	versions     map[string]bool // "tool from swim 0.20261106.1"
	lastFail     string          // its why, or result line
	lastFailAt   string          // the round's start
}

// handCheck is one check the lanes wrote themselves, by its label with
// numbers taken out (sbx1 and sbx2 are one check).
type handCheck struct {
	label        string // the newest label written
	rounds       map[string]bool
	goals        map[string]bool
	fails        int
	lastAt       string
	failedRounds map[string]bool
}

var digitRuns = regexp.MustCompile(`[0-9]+`)

// helperStats is swim helpers stats: every lane log's rounds (archives
// too), read for the steps helpers ran (by their helper: line) and the
// checks lanes keep writing by hand.
func helperStats(root string) error {
	files, _ := filepath.Glob(filepath.Join(lane.LogDir(root), "lane[0-9]*.log"))
	sort.Strings(files)
	uses := map[string]*helperUse{}
	checks := map[string]*handCheck{}
	rounds, first := 0, ""
	for _, f := range files {
		all, err := splitRounds(f)
		if err != nil {
			return err
		}
		for _, lines := range all {
			r, err := logparse.ParseRound(lines)
			if err != nil || !r.Found {
				continue
			}
			rounds++
			if first == "" || (r.StartedAt != "" && r.StartedAt < first) {
				first = r.StartedAt
			}
			for _, res := range r.Results {
				if !res.Step || (res.Kind != logparse.Pass && res.Kind != logparse.Fail && res.Kind != logparse.Blocked) {
					continue
				}
				failed := res.Kind != logparse.Pass
				if fn := res.HelperFunc(); fn != "" {
					u := uses[fn]
					if u == nil {
						u = &helperUse{name: fn, versions: map[string]bool{}}
						uses[fn] = u
					}
					u.calls++
					if _, v, ok := strings.Cut(res.Helper, ", "); ok {
						if _, v, ok = strings.Cut(v, ", "); ok {
							u.versions[v] = true
						}
					}
					if failed {
						u.fails++
						if r.StartedAt >= u.lastFailAt {
							u.lastFailAt, u.lastFail = r.StartedAt, res.Brief()
						}
					}
					continue
				}
				key := strings.ToLower(digitRuns.ReplaceAllString(res.Label, "N"))
				c := checks[key]
				if c == nil {
					c = &handCheck{rounds: map[string]bool{}, goals: map[string]bool{}, failedRounds: map[string]bool{}}
					checks[key] = c
				}
				c.rounds[r.Job] = true
				c.goals[r.Title] = true
				if r.StartedAt >= c.lastAt {
					c.lastAt, c.label = r.StartedAt, res.Label
				}
				if failed {
					c.failedRounds[r.Job] = true
				}
			}
		}
	}
	if rounds == 0 {
		fmt.Println("no rounds in this repo's lane logs yet (.swim/logs)")
		return nil
	}
	fmt.Printf("%d rounds in this repo's lane logs, since %s\n", rounds, dateOf(first))

	fmt.Println()
	if len(uses) == 0 {
		fmt.Println("helpers: none ran (swim helpers lists the modules)")
	} else {
		var us []*helperUse
		for _, u := range uses {
			us = append(us, u)
		}
		sort.Slice(us, func(a, b int) bool {
			if us[a].fails != us[b].fails {
				return us[a].fails > us[b].fails
			}
			if us[a].calls != us[b].calls {
				return us[a].calls > us[b].calls
			}
			return us[a].name < us[b].name
		})
		fmt.Println("helpers, most failures first:")
		fmt.Printf("  %5s  %6s  %-24s  %s\n", "calls", "failed", "helper", "last failure")
		for _, u := range us {
			last := "-"
			if u.fails > 0 {
				last = dateOf(u.lastFailAt) + "  " + u.lastFail
			}
			fmt.Printf("  %5d  %6d  %-24s  %s\n", u.calls, u.fails, u.name, last)
			if len(u.versions) > 1 {
				var vs []string
				for v := range u.versions {
					vs = append(vs, v)
				}
				sort.Strings(vs)
				fmt.Printf("  %5s  %6s  %-24s  ran as: %s\n", "", "", "", strings.Join(vs, "; "))
			}
		}
	}

	var cs []*handCheck
	for _, c := range checks {
		if len(c.rounds) >= candidateRounds && len(c.goals) >= candidateGoals {
			cs = append(cs, c)
		}
	}
	sort.Slice(cs, func(a, b int) bool {
		if len(cs[a].rounds) != len(cs[b].rounds) {
			return len(cs[a].rounds) > len(cs[b].rounds)
		}
		return cs[a].label < cs[b].label
	})
	fmt.Println()
	if len(cs) == 0 {
		fmt.Printf("checks written by hand: none repeated in %d+ rounds of %d+ round goals\n", candidateRounds, candidateGoals)
		return nil
	}
	fmt.Printf("checks written by hand in %d+ rounds of %d+ round goals: candidates for a helper\n", candidateRounds, candidateGoals)
	fmt.Printf("  %6s  %5s  %6s  %s\n", "rounds", "goals", "failed", "check (newest label)")
	for _, c := range cs {
		fmt.Printf("  %6d  %5d  %6d  %s\n", len(c.rounds), len(c.goals), len(c.failedRounds), c.label)
	}
	return nil
}

// dateOf is a round start's date (2026-11-05T21:28:02Z: 2026-11-05).
func dateOf(ts string) string {
	d, _, _ := strings.Cut(ts, "T")
	return d
}
