// Package version defines swim's version, 0.<YYYYMMDD>.<index>, and the
// repo's .swim.lock, which pins the syntax versions of the files a repo's
// lanes use (lane scripts, logs and swim's state).
package version

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chars"
)

// A release is tagged v0.<YYYYMMDD>.<index>: the day, and the release's
// number that day (1, 2, ...), e.g. v0.20261014.2, and reports itself as
// 0.20261014.2. Go needs a /vN module path for majors above 1, so the
// major stays 0. Compatibility isn't in the version: it's the syntax
// versions .swim.lock pins (see Syntaxes).

// Set at build time by the Makefile:
//
//	-X github.com/gates-brightly/swimlane/internal/version.BuildDate=20261009
//	-X github.com/gates-brightly/swimlane/internal/version.Commit=64a0e07
//	-X github.com/gates-brightly/swimlane/internal/version.BuildTag=v0.20261009.1  (HEAD is a release)
var (
	BuildDate = ""
	Commit    = ""
	BuildTag  = ""
)

// TestSyntaxes, set at build time ("script,log,state", e.g. 3,2,2), builds
// a swim that says it reads those syntaxes instead of its own: the tests
// run it as a worker of an older build. A release never sets it.
var TestSyntaxes = ""

// ParseSyntaxes reads "script,log,state" (3,2,2).
func ParseSyntaxes(s string) (Syntaxes, bool) {
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		return Syntaxes{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || v < 1 {
			return Syntaxes{}, false
		}
		n[i] = v
	}
	return Syntaxes{Script: n[0], Log: n[1], State: n[2]}, true
}

// InstallCmd is how users get a current swim.
const InstallCmd = "go install github.com/gates-brightly/swimlane/cmd/swim@latest"

// RepoWeb is swim's repository on the web.
const RepoWeb = "https://github.com/gates-brightly/swimlane"

// Tag is the git tag of release index on date, e.g. "v0.20261009.2".
func Tag(date string, index int) string { return fmt.Sprintf("v0.%s.%d", date, index) }

// ParseTag splits a release tag ("v0.20261009.2") into its date and index.
func ParseTag(tag string) (date string, index string, ok bool) {
	v, isTag := strings.CutPrefix(tag, "v")
	if !isTag {
		return "", "", false
	}
	return ParseVersion(v)
}

// ParseVersion splits a release version ("0.20261009.2") into its date and
// index (no leading zero, 1 or more).
func ParseVersion(v string) (date string, index string, ok bool) {
	p := strings.Split(v, ".")
	if len(p) != 3 || p[0] != "0" || len(p[1]) != 8 || !chars.Digits(p[1]) || !chars.Digits(p[2]) || p[2][0] == '0' {
		return "", "", false
	}
	return p[1], p[2], true
}

// pseudoDate is the date of a Go pseudo-version
// (v0.0.0-20261015123456-abcdef123456), "" if it isn't one.
func pseudoDate(v string) string {
	p := strings.Split(v, "-")
	for i := 1; i+1 < len(p); i++ {
		if len(p[i]) == 14 && chars.Digits(p[i]) && len(p[i+1]) >= 12 && chars.All(p[i+1][:12], chars.Or(chars.Digit, chars.In("abcdef"))) {
			return p[i][:8]
		}
	}
	return ""
}

// release is the release tag this binary was built from: the Makefile's
// BuildTag, or Go's build info (`go install ...@v0.20261009.2` or @latest).
func release() (date, index string, ok bool) {
	if d, i, ok := ParseTag(BuildTag); ok {
		return d, i, true
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		return ParseTag(bi.Main.Version)
	}
	return "", "", false
}

// ReleaseTag is the release tag this binary was built from
// ("v0.20261009.2"), or "" for a build between releases.
func ReleaseTag() string {
	if d, i, ok := release(); ok {
		return "v0." + d + "." + i
	}
	return ""
}

// Date returns the build date as YYYYMMDD: the release's, the Makefile's,
// else from Go's build info (VCS time for a checkout build, or a
// pseudo-version's timestamp for an untagged commit), else "dev".
func Date() string {
	if d, _, ok := release(); ok {
		return d
	}
	if BuildDate != "" {
		return BuildDate
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.time" {
				if t, err := time.Parse(time.RFC3339, s.Value); err == nil {
					return t.UTC().Format("20060102")
				}
			}
		}
		if d := pseudoDate(bi.Main.Version); d != "" {
			return d
		}
	}
	return "dev"
}

// String is the version: "0.20261009.2" for a release, "0.20261009" for a
// build between releases.
func String() string {
	if d, i, ok := release(); ok {
		return "0." + d + "." + i
	}
	return "0." + Date()
}

// Long adds the commit when known, e.g. "0.20261009.2 (64a0e07)".
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

// Syntaxes are the syntax versions of the files a repo's lanes use: lane
// scripts (the newest swim reads), lane logs, and swim's state files
// (status, the chain, the queue, approvals). Scripts and logs also say
// their syntax in the file; state files don't.
type Syntaxes struct {
	Script int `yaml:"script" json:"script"`
	Log    int `yaml:"log" json:"log"`
	State  int `yaml:"state" json:"state"`
}

func (s Syntaxes) String() string {
	return fmt.Sprintf("script %d, log %d, state %d", s.Script, s.Log, s.State)
}

// Current are this swim's syntaxes. The syntax package sets them (it
// imports this one): every swim binary links it.
var Current Syntaxes

// legacy7 is what a lock from before syntax pinning, `breaking: 7`, means:
// the formats of swim 7.20261013 (earlier breaking versions are older).
var legacy7 = Syntaxes{Script: 3, Log: 2, State: 1}

// newer reports whether s is newer than o in any syntax.
// CantRun says why a swim reading s can't run a queued batch in a repo
// whose .swim.lock pins lock (locked false: there's none) and whose lanes'
// scripts declare up to syntax script; "" when it can. It's the rule swim
// applies to itself: the lock pins exactly s (Check), and no lane script
// is newer than s.Script.
func (s Syntaxes) CantRun(lock Syntaxes, locked bool, script int) string {
	switch {
	case locked && lock != s:
		return fmt.Sprintf("it reads %s, and %s pins %s", s, LockName, lock)
	case script > s.Script:
		return fmt.Sprintf("a lane script in the batch declares syntax %d, and it reads up to %d", script, s.Script)
	}
	return ""
}

func (s Syntaxes) newer(o Syntaxes) bool {
	return s.Script > o.Script || s.Log > o.Log || s.State > o.State
}

// LockName is the lock file's name at the repo root.
const LockName = ".swim.lock"

// Lock is the content of .swim.lock.
type Lock struct {
	Syntax    Syntaxes `yaml:"syntax"`
	Version   string   `yaml:"version"`    // swim version that wrote the lock
	UpdatedAt string   `yaml:"updated_at"` // when it was written
	// Breaking is a lock from before syntax pinning (swim 7 and earlier).
	Breaking int `yaml:"breaking,omitempty"`
	// Modules pins the built-in modules the repo's lanes use (swim modules
	// lock). An older swim ignores the key.
	Modules *LockModules `yaml:"modules,omitempty"`
}

// LockModules is the lock's modules: section.
type LockModules struct {
	Builtin map[string]LockedModule `yaml:"builtin,omitempty"`
}

// LockedModule is one pinned module: its version and its files' hash.
type LockedModule struct {
	Version string `yaml:"version"`
	SHA256  string `yaml:"sha256"`
}

// Builtin is the lock's pin of built-in module name (ok false: none).
func (l Lock) Builtin(name string) (LockedModule, bool) {
	if l.Modules == nil {
		return LockedModule{}, false
	}
	m, ok := l.Modules.Builtin[name]
	return m, ok
}

// Syntaxes are the syntaxes the lock pins (a legacy lock's, translated:
// breaking 7 is legacy7, an earlier one older than any syntax).
func (l Lock) Syntaxes() Syntaxes {
	if l.Syntax.Script > 0 || l.Breaking == 0 {
		return l.Syntax
	}
	if l.Breaking == 7 {
		return legacy7
	}
	return Syntaxes{}
}

// pins says what the lock pins, for messages.
func (l Lock) pins() string {
	if l.Syntax.Script == 0 && l.Breaking != 0 {
		return fmt.Sprintf("breaking version %d", l.Breaking)
	}
	return "syntax " + l.Syntaxes().String()
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
	if err := yaml.Unmarshal(data, &l); err != nil || l.Syntax.Script < 1 && l.Breaking < 1 {
		return Lock{}, false, fmt.Errorf("%s is not a valid swim lock (fix or delete it): %v", LockPath(root), err)
	}
	return l, true, nil
}

// WriteLock records this swim's syntaxes in root's lock.
func WriteLock(root string) error {
	l := Lock{Syntax: Current}
	if old, ok, err := ReadLock(root); err == nil && ok {
		l.Modules = old.Modules // what it pins of modules stays
	}
	return writeLock(root, l)
}

// WriteLockModules pins the built-in modules the repo's lanes use (nil:
// none), keeping the syntaxes the lock pins, or this swim's if there's no
// lock.
func WriteLockModules(root string, builtin map[string]LockedModule) error {
	l := Lock{Syntax: Current}
	if old, ok, err := ReadLock(root); err != nil {
		return err
	} else if ok {
		l.Syntax, l.Breaking = old.Syntax, old.Breaking
	}
	if len(builtin) > 0 {
		l.Modules = &LockModules{Builtin: builtin}
	}
	return writeLock(root, l)
}

func writeLock(root string, l Lock) error {
	l.Version, l.UpdatedAt = String(), time.Now().UTC().Format(time.RFC3339)
	data, _ := yaml.Marshal(l)
	head := "# swim lock: the syntax versions of the files this repo's lanes use (lane scripts,\n" +
		"# logs, swim's state), and the built-in modules they use (swim modules lock).\n" +
		"# Written by swim; commit it so everyone runs a compatible swim.\n" +
		"# See `swim lock --help`.\n"
	tmp := LockPath(root) + ".tmp"
	if err := os.WriteFile(tmp, []byte(head+string(data)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, LockPath(root))
}

// Mismatched reports whether the lock pins other syntaxes than this swim's.
func (l Lock) Mismatched() bool { return l.Newer() || l.Syntaxes() != Current }

// Newer reports whether the lock needs a newer swim. A legacy lock above
// breaking 7 is: no swim wrote one before syntax pinning.
func (l Lock) Newer() bool {
	if l.Syntax.Script == 0 && l.Breaking > 7 {
		return true
	}
	return l.Syntaxes().newer(Current)
}

// ErrMismatch explains a syntax mismatch and how to resolve it.
type ErrMismatch struct {
	Lock Lock
	Path string
}

func (e ErrMismatch) Error() string {
	var b strings.Builder
	pinned := e.Lock.pins()
	if e.Lock.Newer() {
		fmt.Fprintf(&b, "this repo needs a newer swim: %s pins %s (written by swim %s),\n", LockName, pinned, e.Lock.Version)
		fmt.Fprintf(&b, "but this swim is %s (syntax %s).\n\n", String(), Current)
		b.WriteString("Update swim, then run again:\n")
		fmt.Fprintf(&b, "  %s\n", InstallCmd)
		b.WriteString("  (or, in a checkout of swimlane: git pull && make install)\n")
		b.WriteString("Check with: swim --version")
		return b.String()
	}
	fmt.Fprintf(&b, "this repo's lanes were set up for an older swim: %s pins %s (written by swim %s),\n", LockName, pinned, e.Lock.Version)
	fmt.Fprintf(&b, "and this swim is %s (syntax %s), which changed lane scripts, logs or state incompatibly.\n\n", String(), Current)
	b.WriteString("Before running:\n")
	b.WriteString("  1. Read what changed: swim lock --help\n")
	b.WriteString("  2. Update the lane scripts (swim new writes the current template) and let running lanes finish.\n")
	b.WriteString("  3. Record that the repo is ready: swim lock --upgrade\n")
	b.WriteString("Or keep using the older swim until then (install the matching version, e.g. @<older tag>).")
	return b.String()
}

// Check returns ErrMismatch if root's lock pins other syntaxes. A missing
// lock is fine.
func Check(root string) error {
	l, ok, err := ReadLock(root)
	if err != nil || !ok {
		return err
	}
	if l.Mismatched() {
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
		if l.Mismatched() {
			return false, ErrMismatch{Lock: l, Path: LockPath(root)}
		}
		return false, nil
	}
	return true, WriteLock(root)
}
