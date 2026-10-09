package ci

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Base is where --changed diffs from. Range is the git diff argument
// ("A..B" style is never used: either "BASE" with HEAD, or "BASE...HEAD"
// for a merge request's merge base). All means there is no usable base (a
// new branch): every pending round runs.
type Base struct {
	Ref   string
	Merge bool // diff from the merge base of Ref and HEAD (pull/merge request)
	All   bool
	Why   string
}

func zeroSHA(s string) bool { return s != "" && strings.Trim(s, "0") == "" }

// FindBase works out --changed's base: explicit (--changed=REF) or from
// the provider's environment.
func FindBase(getenv func(string) string, p Provider, explicit string) (Base, error) {
	if explicit != "" {
		if zeroSHA(explicit) {
			return Base{All: true, Why: "--changed base is the zero SHA (new branch): every pending round"}, nil
		}
		return Base{Ref: explicit, Why: "since " + explicit}, nil
	}
	switch p.Name() {
	case "github":
		if strings.HasPrefix(getenv("GITHUB_EVENT_NAME"), "pull_request") && getenv("GITHUB_BASE_REF") != "" {
			ref := "origin/" + getenv("GITHUB_BASE_REF")
			return Base{Ref: ref, Merge: true, Why: "since the merge base with " + ref}, nil
		}
		before := githubBefore(getenv("GITHUB_EVENT_PATH"))
		switch {
		case zeroSHA(before):
			return Base{All: true, Why: "a new branch (no before SHA): every pending round"}, nil
		case before != "":
			return Base{Ref: before, Why: "since the push's before SHA " + short(before)}, nil
		}
	case "gitlab":
		if b := getenv("CI_MERGE_REQUEST_DIFF_BASE_SHA"); b != "" {
			return Base{Ref: b, Why: "since the merge request's base " + short(b)}, nil
		}
		b := getenv("CI_COMMIT_BEFORE_SHA")
		switch {
		case zeroSHA(b):
			return Base{All: true, Why: "a new branch (CI_COMMIT_BEFORE_SHA is zero): every pending round"}, nil
		case b != "":
			return Base{Ref: b, Why: "since the push's before SHA " + short(b)}, nil
		}
	}
	return Base{}, fmt.Errorf("--changed: no base found in the %s environment; give one: --changed=<sha or ref>", p.Name())
}

// githubBefore reads "before" from the push event payload.
func githubBefore(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var ev struct {
		Before string `json:"before"`
	}
	json.Unmarshal(data, &ev)
	return ev.Before
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

var laneFileRE = regexp.MustCompile(`^lane\.([0-9]+)\.sh$`)

// ChangedLanes returns the lanes whose lane.N.sh differs between the base
// and HEAD, with the changed file names.
func ChangedLanes(root string, b Base) ([]int, []string, error) {
	rng := b.Ref
	if b.Merge {
		rng = b.Ref + "...HEAD"
	}
	c := exec.Command("git", "diff", "--name-only", "--no-renames", rng)
	if !b.Merge {
		c = exec.Command("git", "diff", "--name-only", "--no-renames", rng, "HEAD")
	}
	c.Dir = root
	out, err := c.Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, nil, fmt.Errorf("git diff %s: %s (is the base fetched? actions/checkout needs fetch-depth: 0)", rng, msg)
	}
	var lanes []int
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if m := laneFileRE.FindStringSubmatch(f); m != nil {
			n, _ := strconv.Atoi(m[1])
			lanes = append(lanes, n)
			files = append(files, f)
		}
	}
	sort.Ints(lanes)
	sort.Strings(files)
	return lanes, files, nil
}
