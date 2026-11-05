package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gates-brightly/swimlane/internal/chars"
)

// RepoSlug names the repo's directory in the store: its base name and 6
// hex digits of a hash of its origin remote, so worktrees and clones of
// one repo share it and two repos called api don't. A repo with no remote
// hashes its root path.
func (c *Config) RepoSlug() (string, error) { return repoSlug(c.Root) }

func repoSlug(root string) (string, error) {
	id := remote(root)
	if id == "" {
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", err
		}
		id = abs
	}
	sum := sha256.Sum256([]byte(id))
	name := strings.Trim(chars.Squash(filepath.Base(root), slugChar, "-"), ".-")
	if name == "" {
		name = "repo"
	}
	return name + "-" + hex.EncodeToString(sum[:])[:6], nil
}

// slugChar is a rune a repo's slug keeps.
var slugChar = chars.Or(chars.Alnum, chars.In("._-"))

// remoteFn is git's origin URL for root ("" without one); tests replace
// it. It reads the repo's config file (the common one, which a worktree
// shares) rather than asking git config: swim only reads git.
var remoteFn = func(root string) string {
	c := exec.Command("git", "rev-parse", "--git-common-dir")
	c.Dir = root
	out, err := c.Output()
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		return ""
	}
	return originURL(string(data))
}

// originURL is the url of [remote "origin"] in a git config file.
func originURL(config string) string {
	in := false
	for _, line := range strings.Split(config, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			in = strings.EqualFold(strings.Join(strings.Fields(t), " "), `[remote "origin"]`)
			continue
		}
		if k, v, ok := strings.Cut(t, "="); in && ok && strings.EqualFold(strings.TrimSpace(k), "url") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func remote(root string) string { return strings.TrimSuffix(remoteFn(root), ".git") }
