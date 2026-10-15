package version

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ReleaseRemotes are the git remotes swim version asks for release tags,
// in order: https, then ssh. git asks with the user's own credentials,
// since the repo may be private. SWIM_RELEASES_URL replaces them (a fork,
// a mirror, tests).
func ReleaseRemotes() []string {
	if u := os.Getenv("SWIM_RELEASES_URL"); u != "" {
		return []string{u}
	}
	return []string{RepoWeb + ".git", "git@github.com:gates-brightly/swimlane.git"}
}

// LatestTag asks the remotes for their newest release tag (v0.<date>.<n>)
// with git ls-remote, each within timeout and never prompting. It returns
// the first answer, or the last error.
func LatestTag(timeout time.Duration) (tag string, err error) {
	for _, r := range ReleaseRemotes() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		cmd := exec.CommandContext(ctx, "git", "ls-remote", "--tags", "--refs", r)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0",
			"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=5")
		out, e := cmd.Output()
		timedOut := ctx.Err() != nil
		cancel()
		switch {
		case timedOut:
			err = fmt.Errorf("%s: no answer in %s", r, timeout)
		case e != nil:
			err = fmt.Errorf("%s: %s", r, gitErr(e))
		default:
			if t := NewestTag(string(out)); t != "" {
				return t, nil
			}
			err = fmt.Errorf("%s: no release tags", r)
		}
	}
	return "", err
}

// gitErr is the first line git wrote to stderr, else the error.
func gitErr(e error) string {
	var ee *exec.ExitError
	if errors.As(e, &ee) {
		if l := strings.TrimSpace(strings.SplitN(string(ee.Stderr), "\n", 2)[0]); l != "" {
			return l
		}
	}
	return e.Error()
}

// NewestTag picks the newest release tag from git ls-remote --tags output
// ("<sha>\trefs/tags/<tag>" lines), ignoring tags that aren't releases.
func NewestTag(lsRemote string) string {
	best, bd, bi := "", "", 0
	for _, line := range strings.Split(lsRemote, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		d, is, ok := ParseTag(strings.TrimPrefix(f[1], "refs/tags/"))
		if !ok {
			continue
		}
		i, _ := strconv.Atoi(is)
		if d > bd || d == bd && i > bi {
			best, bd, bi = strings.TrimPrefix(f[1], "refs/tags/"), d, i
		}
	}
	return best
}

// Compare says how release tag stands against this build: 1 if the tag is
// newer, 0 if it's this release, -1 if this build is newer. ok is false
// when that can't be told: a build between releases from the tag's day, or
// a build with no date.
func Compare(tag string) (cmp int, ok bool) {
	td, tis, ok := ParseTag(tag)
	if !ok {
		return 0, false
	}
	ti, _ := strconv.Atoi(tis)
	if d, is, ok := release(); ok {
		i, _ := strconv.Atoi(is)
		return compareRelease(td, ti, d, i), true
	}
	switch d := Date(); {
	case d == "dev" || td == d:
		return 0, false
	case td > d:
		return 1, true
	}
	return -1, true
}

// compareRelease orders release (td, ti) against (d, i).
func compareRelease(td string, ti int, d string, i int) int {
	switch {
	case td > d || td == d && ti > i:
		return 1
	case td == d && ti == i:
		return 0
	}
	return -1
}
