package config

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// RepoName is the repo's name: the origin remote URL's last path segment without .git, else the git toplevel's basename, else dir's basename.
func RepoName(dir string) string {
	if u := gitOut(exec.Command("git", "remote", "get-url", "origin"), dir); u != "" {
		u = strings.TrimRight(u, "/")
		if i := strings.LastIndexAny(u, "/:"); i >= 0 {
			u = u[i+1:]
		}
		if u = strings.TrimSuffix(u, ".git"); u != "" {
			return u
		}
	}
	if top := gitOut(exec.Command("git", "rev-parse", "--show-toplevel"), dir); top != "" {
		return filepath.Base(top)
	}
	return filepath.Base(dir)
}

// RepoBranch is the checked-out branch, "" when detached or outside git.
func RepoBranch(dir string) string {
	return gitOut(exec.Command("git", "symbolic-ref", "--short", "-q", "HEAD"), dir)
}

// gitOut runs a git command in dir and returns its trimmed output, "" on
// any error.
func gitOut(cmd *exec.Cmd, dir string) string {
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
