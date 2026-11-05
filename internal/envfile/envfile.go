// Package envfile reads the env files that give lanes their environment:
// declarative KEY=VALUE files, parsed and never sourced, in two files.
//
//	~/.config/swim/env.swim   general keys, then [repo NAME] and [repo NAME/BRANCH] sections
//	<repo>/.env.swim          [lane N] sections only (git-ignored)
//
// Precedence, highest first: the shell, then lane, the repo's branch, the
// repo, general. NAME is the repo's name (config.RepoName), as config.yml's
// repos: keys it, so every worktree of a repo shares its sections.
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
	"sort"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
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
	Repos   []Section // [repo NAME] and [repo NAME/BRANCH], in file order
	Lanes   map[int][]Entry
	// Dups are keys set twice in one section (the last wins).
	Dups []Dup
}

// Dup is a key set again in the same section of a file.
type Dup struct {
	Key         string
	Line, First int
	Section     string // "general", "repo NAME", "lane N"
}

// Section is a [repo NAME] or [repo NAME/BRANCH] section.
type Section struct {
	Key     string // NAME or NAME/BRANCH, as written
	Entries []Entry
}

// Parse reads an env file. user is the operator's file (general keys and
// [repo NAME] sections); otherwise it's a repo's .env.swim ([lane N]
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
	seen := map[string]int{} // section + key -> first line
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := section(line); m != nil {
			switch {
			case m[1] == "repo" && user:
				f.Repos = append(f.Repos, Section{Key: m[2]})
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
			return nil, bad(n, "%q: sections are [repo NAME] (in %s) or [lane N] (in %s)", line, UserPath(), RepoFile)
		}
		if !general && repoSec < 0 && laneSec == 0 {
			return nil, bad(n, "a key before any [lane N] section: this file holds lane sections only (general and repo keys go in %s)", UserPath())
		}
		if strings.HasPrefix(line, "export ") {
			return nil, bad(n, "no `export`: env files are KEY=VALUE lines, never run as shell")
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || !chars.Ident(k) {
			return nil, bad(n, "expected KEY=VALUE (letters, digits and _ in the key)")
		}
		val, err := value(strings.TrimSpace(v))
		if err != nil {
			return nil, bad(n, "%s: %v", k, err)
		}
		e := Entry{Key: k, Value: val, Line: n}
		section := "general"
		switch {
		case laneSec > 0:
			section = fmt.Sprintf("lane %d", laneSec)
		case repoSec >= 0:
			section = "repo " + f.Repos[repoSec].Key
		}
		if first, dup := seen[section+"\x00"+k]; dup {
			f.Dups = append(f.Dups, Dup{Key: k, Line: n, First: first, Section: section})
		} else {
			seen[section+"\x00"+k] = n
		}
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
	Scope      string // "lane 2", "repo/branch", "repo", "general"
	File       string // the file, as people read it: .env.swim, ~/.config/swim/env.swim
	Line       int
}

// Resolved is the env files' environment for one lane.
type Resolved struct {
	Vars    []Var    // sorted by key
	Sources []string // what applied, for the round header: "~/.config/swim/env.swim[repo]"
}

// Resolve reads the env files for lane n of the repo at root.
func Resolve(root string, n int) (*Resolved, error) {
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
	if user != nil && len(user.Repos) > 0 {
		name := config.RepoName(root)
		keys := [][2]string{{name, "repo"}}
		if br := config.RepoBranch(root); br != "" {
			keys = append(keys, [2]string{name + "/" + br, "repo/branch"})
		}
		for _, k := range keys {
			for _, sec := range user.Repos {
				if sec.Key == k[0] {
					add(k[1], short, short+"["+k[1]+"]", sec.Entries)
				}
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

// PathSections are the [repo ...] sections of the operator's env file
// whose key is a path (/abs/path, ~/src/x), with their lines: swim keyed
// them by the repo's directory once; it keys them by name now, so these
// match nothing. A missing or unparsable file has none.
func PathSections() []string {
	data, err := os.ReadFile(UserPath())
	if err != nil {
		return nil
	}
	f, err := Parse(UserPath(), data, true)
	if err != nil {
		return nil
	}
	var out []string
	for _, sec := range f.Repos {
		if filepath.IsAbs(sec.Key) || sec.Key == "~" || strings.HasPrefix(sec.Key, "~/") {
			out = append(out, sec.Key)
		}
	}
	return out
}

// GuardConflict is an env file key that is a guard flag.
type GuardConflict struct {
	Var
	Why string // "a guard flag of this lane's script"
}

func (g GuardConflict) Error() string {
	where := g.File
	if g.Line > 0 {
		where = fmt.Sprintf("%s:%d", g.File, g.Line)
	}
	return fmt.Sprintf("%s sets %s, %s: env files can't approve guards (set it in the shell, or approve at the prompt)", where, g.Key, g.Why)
}

// GuardConflicts lists the keys of r that are guard flags: guards of the
// lane's script (header or body).
func GuardConflicts(r *Resolved, info lane.Info) []GuardConflict {
	guards := map[string]bool{}
	for _, f := range info.GuardFlags() {
		guards[f] = true
	}
	var out []GuardConflict
	for _, v := range r.Vars {
		if guards[v.Key] {
			out = append(out, GuardConflict{v, "a guard flag of this lane's script"})
		}
	}
	return out
}

// Check resolves lane n's env and refuses guard flags in it: what the
// launcher does before any lane starts.
func Check(root string, n int, info lane.Info) (*Resolved, error) {
	r, err := Resolve(root, n)
	if err != nil {
		return nil, err
	}
	if c := GuardConflicts(r, info); len(c) > 0 {
		return nil, fmt.Errorf("swim %d: %w", n, c[0])
	}
	return r, nil
}

// Duplicates lists keys set twice in a section of the env files that apply
// to the repo at root (the operator's and the repo's), as "file:line" notes.
func Duplicates(root string) []string {
	var out []string
	for _, src := range []struct {
		path string
		user bool
	}{{UserPath(), true}, {filepath.Join(root, RepoFile), false}} {
		data, err := os.ReadFile(src.path)
		if err != nil {
			continue
		}
		f, err := Parse(src.path, data, src.user)
		if err != nil {
			continue
		}
		for _, d := range f.Dups {
			out = append(out, fmt.Sprintf("%s:%d sets %s again in [%s] (first at line %d; the last wins)", tilde(src.path), d.Line, d.Key, d.Section, d.First))
		}
	}
	return out
}

// section reads a "[repo NAME]" or "[lane N]" line: the kind and its
// argument, or nil.
func section(line string) []string {
	body, ok := chars.Between(line, "[", "]")
	if !ok {
		return nil
	}
	kind, arg, ok := strings.Cut(strings.TrimSpace(body), " ")
	arg = strings.TrimSpace(arg)
	if !ok || arg == "" || kind != "repo" && kind != "lane" {
		return nil
	}
	return []string{line, kind, arg}
}
