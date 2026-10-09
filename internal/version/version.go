// Package version defines swim's version, <breaking>.<YYYYMMDD>, and the
// repo's .swim.lock, which pins the breaking version a repo's lanes use.
package version

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Breaking is bumped when lane scripts, logs or swim's state files change
// incompatibly, and for a second release on the same day (a tag is
// v0.<breaking>.<YYYYMMDD>, so there is one release per breaking version per
// day). Repos locked to another breaking version refuse to run.
const Breaking = 4

// Set at build time by the Makefile:
//
//	-X github.com/gates-brightly/swimlane/internal/version.BuildDate=20261009
//	-X github.com/gates-brightly/swimlane/internal/version.Commit=64a0e07
var (
	BuildDate = ""
	Commit    = ""
)

// InstallCmd is how users get a current swim.
const InstallCmd = "go install github.com/gates-brightly/swimlane/cmd/swim@latest"

var (
	pseudoRE = regexp.MustCompile(`-(\d{8})\d{6}-[0-9a-f]{12}`)
	// Release tags are v0.<breaking>.<YYYYMMDD>: Go needs a /vN module path
	// for majors above 1, so swim's own breaking number goes in the minor.
	tagRE = regexp.MustCompile(`^v0\.(\d+)\.(\d{8})$`)
)

// Tag is the git tag for this version, e.g. "v0.2.20261009".
func Tag(date string) string { return fmt.Sprintf("v0.%d.%s", Breaking, date) }

// Date returns the build date as YYYYMMDD: from the Makefile, else from Go's
// build info (the release tag's date for `go install ...@v0.2.20261009` or
// @latest, VCS time for a checkout build, or a pseudo-version's timestamp
// for an untagged commit), else "dev".
func Date() string {
	if BuildDate != "" {
		return BuildDate
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if m := tagRE.FindStringSubmatch(bi.Main.Version); m != nil {
			return m[2]
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.time" {
				if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
					return t.UTC().Format("20060102")
				}
			}
		}
		if m := pseudoRE.FindStringSubmatch(bi.Main.Version); m != nil {
			return m[1]
		}
	}
	return "dev"
}

// TagMismatch returns the release tag this binary was built from when the
// tag's breaking number isn't Breaking (a release tagged without bumping
// Breaking, like v0.3.20261009), else "".
func TagMismatch() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return tagMismatch(bi.Main.Version)
}

func tagMismatch(mainVersion string) string {
	if m := tagRE.FindStringSubmatch(mainVersion); m != nil && m[1] != fmt.Sprint(Breaking) {
		return mainVersion
	}
	return ""
}

// String is the version, e.g. "2.20261009".
func String() string { return fmt.Sprintf("%d.%s", Breaking, Date()) }

// Long adds the commit when known, e.g. "2.20261009 (64a0e07)".
func Long() string {
	c := Commit
	if c == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			for _, s := range bi.Settings {
				if s.Key == "vcs.revision" && len(s.Value) >= 7 {
					c = s.Value[:7]
				}
			}
		}
	}
	if c == "" {
		return String()
	}
	return String() + " (" + c + ")"
}

// LockName is the lock file's name at the repo root.
const LockName = ".swim.lock"

// Lock is the content of .swim.lock.
type Lock struct {
	Breaking  int    `yaml:"breaking"`
	Version   string `yaml:"version"`    // swim version that wrote the lock
	UpdatedAt string `yaml:"updated_at"` // when it was written
}

// LockPath returns root's lock file path.
func LockPath(root string) string { return filepath.Join(root, LockName) }

// ReadLock reads root's lock. ok is false when there is none.
func ReadLock(root string) (l Lock, ok bool, err error) {
	data, err := os.ReadFile(LockPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return Lock{}, false, nil
	}
	if err != nil {
		return Lock{}, false, err
	}
	if err := yaml.Unmarshal(data, &l); err != nil || l.Breaking < 1 {
		return Lock{}, false, fmt.Errorf("%s is not a valid swim lock (fix or delete it): %v", LockPath(root), err)
	}
	return l, true, nil
}

// WriteLock records this swim's breaking version in root's lock.
func WriteLock(root string) error {
	l := Lock{Breaking: Breaking, Version: String(), UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	data, _ := yaml.Marshal(l)
	head := "# swim lock: the breaking version of swim this repo's lanes use.\n" +
		"# Written by swim; commit it so everyone runs a compatible swim. See `swim lock --help`.\n"
	tmp := LockPath(root) + ".tmp"
	if err := os.WriteFile(tmp, []byte(head+string(data)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, LockPath(root))
}

// ErrMismatch explains a breaking-version mismatch and how to resolve it.
type ErrMismatch struct {
	Lock Lock
	Path string
}

func (e ErrMismatch) Error() string {
	var b strings.Builder
	if e.Lock.Breaking > Breaking {
		fmt.Fprintf(&b, "this repo needs a newer swim: %s pins breaking version %d (written by swim %s),\n", LockName, e.Lock.Breaking, e.Lock.Version)
		fmt.Fprintf(&b, "but this swim is %s (breaking version %d).\n\n", String(), Breaking)
		b.WriteString("Update swim, then run again:\n")
		fmt.Fprintf(&b, "  %s\n", InstallCmd)
		b.WriteString("  (or, in a checkout of swimlane: git pull && make install)\n")
		b.WriteString("Check with: swim --version")
		return b.String()
	}
	fmt.Fprintf(&b, "this repo's lanes were set up for an older swim: %s pins breaking version %d (written by swim %s),\n", LockName, e.Lock.Breaking, e.Lock.Version)
	fmt.Fprintf(&b, "and this swim is %s (breaking version %d), which changed lane scripts, logs or state incompatibly.\n\n", String(), Breaking)
	b.WriteString("Before running:\n")
	b.WriteString("  1. Read what changed: swim lock --help\n")
	b.WriteString("  2. Update the lane scripts (swim new writes the current template) and let running lanes finish.\n")
	b.WriteString("  3. Record that the repo is ready: swim lock --upgrade\n")
	b.WriteString("Or keep using the older swim until then (install the matching version, e.g. @<older tag>).")
	return b.String()
}

// Check returns ErrMismatch if root's lock pins another breaking version.
// A missing lock is fine.
func Check(root string) error {
	l, ok, err := ReadLock(root)
	if err != nil || !ok {
		return err
	}
	if l.Breaking != Breaking {
		return ErrMismatch{Lock: l, Path: LockPath(root)}
	}
	return nil
}

// Ensure checks root's lock and creates it if missing. It reports whether
// it created one.
func Ensure(root string) (created bool, err error) {
	l, ok, err := ReadLock(root)
	if err != nil {
		return false, err
	}
	if ok {
		if l.Breaking != Breaking {
			return false, ErrMismatch{Lock: l, Path: LockPath(root)}
		}
		return false, nil
	}
	return true, WriteLock(root)
}
