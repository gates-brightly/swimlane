// Package lookup reads lookup.yml, the repo's committed list of who is
// who: users, each with the SSH public keys they sign with, and groups of
// users (a group may include other groups). It turns a signature's key
// into a person, and says which approvals a guard needs.
//
//	users:
//	  zach:
//	    keys:
//	      - ssh-ed25519 AAAA... zach@laptop
//	      - sk-ssh-ed25519@openssh.com AAAA... zach@yubikey
//	  alice:
//	    keys: [ssh-ed25519 AAAA... alice@work]
//	groups:
//	  platform-admin: [zach, alice]
//	  ops: [bob, platform-admin]
//	require:                       # signed approvals (.signature files)
//	  - guards: [FIN_ALLOW_*]      # globs; first matching rule applies
//	    group: platform-admin
//	    count: 2                   # distinct users from the group
//	  - group: ops                 # no guards: every other guard
package lookup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is lookup.yml at the repo root.
const FileName = "lookup.yml"

// User is one person and their keys.
type User struct {
	Keys  []string `yaml:"keys"`
	Email string   `yaml:"email,omitempty"`
}

// Rule says what signatures guards need.
type Rule struct {
	Guards []string `yaml:"guards,omitempty"` // globs; empty: any guard
	Group  string   `yaml:"group,omitempty"`  // empty: any known user
	Count  int      `yaml:"count,omitempty"`  // distinct users; default 1
}

// Lookup is lookup.yml.
type Lookup struct {
	Users   map[string]User     `yaml:"users"`
	Groups  map[string][]string `yaml:"groups"`
	Require []Rule              `yaml:"require,omitempty"`

	Path  string            `yaml:"-"` // "" if the repo has none
	byKey map[string]string // "type base64" -> user
}

// Load reads root's lookup.yml; a repo without one gets an empty Lookup.
func Load(root string) (*Lookup, error) {
	l := &Lookup{Users: map[string]User{}, Groups: map[string][]string{}, byKey: map[string]string{}}
	path := filepath.Join(root, FileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, l); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	l.Path = path
	if l.Users == nil {
		l.Users = map[string]User{}
	}
	if l.Groups == nil {
		l.Groups = map[string][]string{}
	}
	l.byKey = map[string]string{}
	for name, u := range l.Users {
		if _, clash := l.Groups[name]; clash {
			return nil, fmt.Errorf("%s: %q is both a user and a group", path, name)
		}
		for _, k := range u.Keys {
			kk := keyOnly(k)
			if other, dup := l.byKey[kk]; dup && other != name {
				return nil, fmt.Errorf("%s: a key is listed for both %s and %s", path, other, name)
			}
			l.byKey[kk] = name
		}
	}
	for g := range l.Groups {
		if _, err := l.members(g, map[string]bool{}); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	for i, r := range l.Require {
		if r.Group != "" {
			if _, ok := l.Groups[r.Group]; !ok {
				return nil, fmt.Errorf("%s: require[%d]: no group %q", path, i, r.Group)
			}
		}
	}
	return l, nil
}

func keyOnly(pub string) string {
	f := strings.Fields(pub)
	if len(f) >= 2 {
		return f[0] + " " + f[1]
	}
	return strings.TrimSpace(pub)
}

// Who is the user a public key belongs to.
func (l *Lookup) Who(pub string) (string, bool) {
	u, ok := l.byKey[keyOnly(pub)]
	return u, ok
}

func (l *Lookup) members(g string, seen map[string]bool) ([]string, error) {
	if seen[g] {
		return nil, fmt.Errorf("group %q includes itself", g)
	}
	seen[g] = true
	defer delete(seen, g)
	set := map[string]bool{}
	for _, m := range l.Groups[g] {
		switch {
		case l.Groups[m] != nil:
			sub, err := l.members(m, seen)
			if err != nil {
				return nil, err
			}
			for _, u := range sub {
				set[u] = true
			}
		default:
			if _, ok := l.Users[m]; !ok {
				return nil, fmt.Errorf("group %q: no user or group %q", g, m)
			}
			set[m] = true
		}
	}
	var out []string
	for u := range set {
		out = append(out, u)
	}
	sort.Strings(out)
	return out, nil
}

// Members are a group's users, nested groups included.
func (l *Lookup) Members(g string) []string {
	m, _ := l.members(g, map[string]bool{})
	return m
}

// GroupsOf are the groups a user belongs to (directly or through others).
func (l *Lookup) GroupsOf(user string) []string {
	var out []string
	for g := range l.Groups {
		for _, m := range l.Members(g) {
			if m == user {
				out = append(out, g)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// RuleFor is the rule a guard's approval must meet: the first whose
// globs match it (or that has none). With no rules, one signature from any
// user in lookup.yml.
func (l *Lookup) RuleFor(flag string) Rule {
	for _, r := range l.Require {
		if len(r.Guards) == 0 {
			return withDefaults(r)
		}
		for _, g := range r.Guards {
			if ok, _ := filepath.Match(g, flag); ok {
				return withDefaults(r)
			}
		}
	}
	return Rule{Count: 1}
}

func withDefaults(r Rule) Rule {
	if r.Count < 1 {
		r.Count = 1
	}
	return r
}

// Satisfied reports whether users (distinct signers known to lookup.yml)
// meet rule, and if not, what's missing.
func (l *Lookup) Satisfied(r Rule, users []string) (bool, string) {
	have := map[string]bool{}
	for _, u := range users {
		if r.Group == "" {
			have[u] = true
			continue
		}
		for _, m := range l.Members(r.Group) {
			if m == u {
				have[u] = true
			}
		}
	}
	if len(have) >= r.Count {
		return true, ""
	}
	who := "users in lookup.yml"
	if r.Group != "" {
		who = r.Group
	}
	return false, fmt.Sprintf("needs %d signature(s) from %s, has %d", r.Count, who, len(have))
}

// Describe is "zach (platform-admin, ops)".
func (l *Lookup) Describe(user string) string {
	if g := l.GroupsOf(user); len(g) > 0 {
		return user + " (" + strings.Join(g, ", ") + ")"
	}
	return user
}
