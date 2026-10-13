// Package envfile reads the env files that give lanes their environment:
// declarative KEY=VALUE files, parsed and never sourced, in three scopes.
//
//	~/.config/swim/env.swim   general keys, then [repo PATH] sections
//	<repo>/.env.swim          [lane N] sections only (git-ignored)
//	<repo>/swim.yml env:      shared, non-secret defaults (committed)
//
// Precedence, highest first: the shell, then lane, repo, swim.yml, general.
// No key may be a guard flag: approvals never come from a file.
package envfile

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
)

// RepoFile is the repo's lane-scoped env file.
const RepoFile = ".env.swim"

// UserPath is the operator's env file, next to config.yml.
func UserPath() string { return filepath.Join(filepath.Dir(config.Path()), "env.swim") }

// Entry is one KEY=VALUE line.
type Entry struct {
	Key, Value string
	Line       int
}

// File is one parsed env file.
type File struct {
	Path    string
	General []Entry
	Repos   []Section // [repo PATH], in file order
	Lanes   map[int][]Entry
}

// Section is a [repo PATH] section.
type Section struct {
	Path    string
	Entries []Entry
}

var (
	keyRE     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sectionRE = regexp.MustCompile(`^\[\s*(repo|lane)\s+(.+?)\s*\]$`)
)

// Parse reads an env file. user is the operator's file (general keys and
// [repo PATH] sections); otherwise it's a repo's .env.swim ([lane N]
// sections only). A line that doesn't parse is an error naming the file
// and line.
func Parse(path string, data []byte, user bool) (*File, error) {
	f := &File{Path: path, Lanes: map[int][]Entry{}}
	bad := func(n int, format string, a ...any) error {
		return fmt.Errorf("%s:%d: %s", path, n, fmt.Sprintf(format, a...))
	}
	// Where keys go: general (the user file, before any section), the
	// current [repo] section, or the current [lane N] section.
	general, repoSec, laneSec := user, -1, 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := sectionRE.FindStringSubmatch(line); m != nil {
			switch {
			case m[1] == "repo" && user:
				f.Repos = append(f.Repos, Section{Path: m[2]})
				general, repoSec = false, len(f.Repos)-1
			case m[1] == "lane" && !user:
				k, err := strconv.Atoi(m[2])
				if err != nil || k < 1 {
					return nil, bad(n, "[lane %s]: a lane is a number", m[2])
				}
				laneSec = k
			case m[1] == "repo":
				return nil, bad(n, "[repo ...] sections go in %s; this file holds [lane N] sections", UserPath())
			default:
				return nil, bad(n, "[lane N] sections go in the repo's %s", RepoFile)
			}
			continue
		}
		if strings.HasPrefix(line, "[") {
			return nil, bad(n, "%q: sections are [repo PATH] (in %s) or [lane N] (in %s)", line, UserPath(), RepoFile)
		}
		if !general && repoSec < 0 && laneSec == 0 {
			return nil, bad(n, "a key before any [lane N] section: this file holds lane sections only (general and repo keys go in %s)", UserPath())
		}
		if strings.HasPrefix(line, "export ") {
			return nil, bad(n, "no `export`: env files are KEY=VALUE lines, never run as shell")
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !keyRE.MatchString(k) {
			return nil, bad(n, "expected KEY=VALUE (letters, digits and _ in the key)")
		}
		val, err := value(strings.TrimSpace(v))
		if err != nil {
			return nil, bad(n, "%s: %v", k, err)
		}
		e := Entry{Key: k, Value: val, Line: n}
		switch {
		case laneSec > 0:
			f.Lanes[laneSec] = append(f.Lanes[laneSec], e)
		case repoSec >= 0:
			f.Repos[repoSec].Entries = append(f.Repos[repoSec].Entries, e)
		default:
			f.General = append(f.General, e)
		}
	}
	return f, sc.Err()
}

// value unquotes a value: '...' is literal, "..." is literal too (no
// escapes), and an unquoted value may not ask for expansion.
func value(v string) (string, error) {
	if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') {
		if v[len(v)-1] != v[0] {
			return "", errors.New("unterminated quote")
		}
		return v[1 : len(v)-1], nil
	}
	if strings.ContainsAny(v, "'\"") {
		return "", errors.New("quote the whole value, or none of it")
	}
	if strings.Contains(v, "$") || strings.Contains(v, "`") {
		return "", errors.New("no expansion ($VAR, $(...), `...`): values are literal; write '...' to mean the characters themselves")
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i]) // a trailing comment
	}
	return v, nil
}

// Var is one resolved key: its value and where it came from.
type Var struct {
	Key, Value string
	Scope      string // "lane 2", "repo", "swim.yml", "general"
	File       string // the file, as people read it: .env.swim, swim.yml, ~/.config/swim/env.swim
	Line       int    // 0 for swim.yml
}

// Resolved is the env files' environment for one lane.
type Resolved struct {
	Vars    []Var    // sorted by key
	Sources []string // what applied, for the round header: "~/.config/swim/env.swim[repo]"
}

// Resolve reads the env files for lane n of the repo at root.
func Resolve(root string, cfg *config.Config, n int) (*Resolved, error) {
	byKey := map[string]Var{}
	var sources []string
	add := func(scope, file, label string, entries []Entry) {
		if len(entries) == 0 {
			return
		}
		for _, e := range entries {
			byKey[e.Key] = Var{Key: e.Key, Value: e.Value, Scope: scope, File: file, Line: e.Line}
		}
		sources = append(sources, label)
	}
	// Lowest precedence first: later layers overwrite.
	var user *File
	if data, err := os.ReadFile(UserPath()); err == nil {
		if user, err = Parse(UserPath(), data, true); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	short := tilde(UserPath())
	if user != nil {
		add("general", short, short, user.General)
	}
	if len(cfg.Env) > 0 {
		var entries []Entry
		for k, v := range cfg.Env {
			if !keyRE.MatchString(k) {
				return nil, fmt.Errorf("%s env: %q is not a variable name", config.RepoFileName, k)
			}
			entries = append(entries, Entry{Key: k, Value: v})
		}
		sort.Slice(entries, func(a, b int) bool { return entries[a].Key < entries[b].Key })
		add("swim.yml", config.RepoFileName, config.RepoFileName+"[env]", entries)
	}
	if user != nil {
		for _, sec := range user.Repos {
			if config.SameRepo(sec.Path, root) {
				add("repo", short, short+"[repo]", sec.Entries)
			}
		}
	}
	repoPath := filepath.Join(root, RepoFile)
	if data, err := os.ReadFile(repoPath); err == nil {
		rf, err := Parse(repoPath, data, false)
		if err != nil {
			return nil, err
		}
		add(fmt.Sprintf("lane %d", n), RepoFile, fmt.Sprintf("%s[lane %d]", RepoFile, n), rf.LaneEntries(n))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	r := &Resolved{Sources: sources}
	for _, v := range byKey {
		r.Vars = append(r.Vars, v)
	}
	sort.Slice(r.Vars, func(a, b int) bool { return r.Vars[a].Key < r.Vars[b].Key })
	return r, nil
}

// LaneEntries are lane k's entries (every [lane k] section, in order).
func (f *File) LaneEntries(k int) []Entry { return f.Lanes[k] }

func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// Env is the resolved environment as KEY=VALUE.
func (r *Resolved) Env() []string {
	out := make([]string, len(r.Vars))
	for i, v := range r.Vars {
		out[i] = v.Key + "=" + v.Value
	}
	return out
}

// SHA256 hashes the resolved env (sorted KEY=VALUE lines); "" if empty.
// The same script with the same env hash ran with the same inputs.
func (r *Resolved) SHA256() string {
	if r == nil || len(r.Vars) == 0 {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(r.Env(), "\n")+"\n")))
}

// GuardConflict is an env file key that is a guard flag.
type GuardConflict struct {
	Var
	Why string // "a guard of lane.2.sh", "matches guards: FIN_ALLOW_* in lookup.yml"
}

func (g GuardConflict) Error() string {
	where := g.File
	if g.Line > 0 {
		where = fmt.Sprintf("%s:%d", g.File, g.Line)
	}
	return fmt.Sprintf("%s sets %s, %s: env files can't approve guards (set it in the shell, approve at the prompt, or sign an approval)", where, g.Key, g.Why)
}

// GuardConflicts lists the keys of r that are guard flags: guards of the
// lane's script (header or body), or names matching a guards: glob in
// lookup.yml's require rules.
func GuardConflicts(r *Resolved, info lane.Info, lk *lookup.Lookup) []GuardConflict {
	guards := map[string]bool{}
	for _, f := range info.GuardFlags() {
		guards[f] = true
	}
	var out []GuardConflict
	for _, v := range r.Vars {
		if guards[v.Key] {
			out = append(out, GuardConflict{v, "a guard flag of this lane's script"})
			continue
		}
		if lk == nil {
			continue
		}
		for _, rule := range lk.Require {
			for _, g := range rule.Guards {
				if ok, _ := filepath.Match(g, v.Key); ok {
					out = append(out, GuardConflict{v, fmt.Sprintf("which matches guards %q in %s", g, lookup.FileName)})
					goto next
				}
			}
		}
	next:
	}
	return out
}

// Check resolves lane n's env and refuses guard flags in it: what the
// launcher does before any lane starts.
func Check(root string, cfg *config.Config, n int, info lane.Info, lk *lookup.Lookup) (*Resolved, error) {
	r, err := Resolve(root, cfg, n)
	if err != nil {
		return nil, err
	}
	if c := GuardConflicts(r, info, lk); len(c) > 0 {
		return nil, fmt.Errorf("swim %d: %w", n, c[0])
	}
	return r, nil
}

// MigrateRC reads a .lane.N.rc as plain assignments (KEY=VALUE, or export
// KEY=VALUE; comments and blank lines), for moving it into a [lane N]
// section. Anything else (commands, expansion, conditionals) is returned
// as a problem: that rc needs migrating by hand.
func MigrateRC(data []byte) ([]Entry, []string) {
	var out []Entry
	var problems []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok || !keyRE.MatchString(k) {
			problems = append(problems, fmt.Sprintf("line %d is not a plain assignment: %s", n, sc.Text()))
			continue
		}
		val, err := value(v)
		if err != nil {
			problems = append(problems, fmt.Sprintf("line %d: %s: %v", n, k, err))
			continue
		}
		out = append(out, Entry{Key: k, Value: val, Line: n})
	}
	return out, problems
}

// Quote writes a value so Parse reads it back unchanged.
func Quote(v string) string {
	if v == "" || strings.ContainsAny(v, " \t#$`\"'") {
		if !strings.Contains(v, "'") {
			return "'" + v + "'"
		}
		return `"` + v + `"`
	}
	return v
}

// AppendLane adds a [lane n] section with entries to the repo's .env.swim
// (created if missing).
func AppendLane(root string, n int, entries []Entry, comment string) error {
	path := filepath.Join(root, RepoFile)
	var b strings.Builder
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		b.WriteString("# Lane env for swim (KEY=VALUE, never run as shell; no guard flags).\n# Precedence: the shell, then [lane N] here, then ~/.config/swim/env.swim.\n")
	} else if err != nil {
		return err
	} else {
		b.Write(data)
		if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "\n[lane %d]\n", n)
	if comment != "" {
		b.WriteString("# " + comment + "\n")
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "%s=%s\n", e.Key, Quote(e.Value))
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
