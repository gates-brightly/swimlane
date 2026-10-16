// Package module is swim's modules: small, versioned utilities a lane
// declares (# Modules: notify clone@1 ./modules/x) and calls as steps (mod,
// mod_env, the SDKs' module op). A module is a bash file of functions or a
// Go program, with a module.yml that says what it is and what it does.
//
// Modules are built into swim, or live in the repo (modules/<name>/). A
// lane's modules are pinned into its execution manifest by hash, so an
// approval covers a module's bytes as it covers the script's. swim.yml's
// modules: decides which modules lanes may use.
//
// Effects (network, secrets, writes, credentials) are declared, not
// enforced: a module runs as the lane, with its permissions. What swim
// enforces is that the bytes that run are the ones approved.
package module

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
)

// Schema identifies a module.yml.
const Schema = "swim.module/v1"

// SpecFile is a module's description, at the top of its directory.
const SpecFile = "module.yml"

// Kinds of module.
const (
	Bash = "bash" // a bash file of functions, <name>.sh
	Go   = "go"   // one Go program (main.go and go.mod in the repo)
)

// GateExit is how swim _module exits when a gate module (gate: true)
// fails: mod and the SDKs' op module then stop the round.
const GateExit = 88

// Sources of a module, as a manifest pins them.
const BuiltinSource = "builtin"

// Effects are what a module says it does. swim shows them in swim plan,
// swim modules and lint; it can't enforce them.
type Effects struct {
	Network     []string `yaml:"network,omitempty"`     // hosts it talks to
	Secrets     []string `yaml:"secrets,omitempty"`     // secret names it asks the broker for
	Writes      []string `yaml:"writes,omitempty"`      // paths outside $SWIM_OUT it may write
	Credentials bool     `yaml:"credentials,omitempty"` // it produces credentials (mod_env)
	ReadOnly    bool     `yaml:"read_only,omitempty"`   // safe before stage change
}

// Spec is a module.yml.
type Spec struct {
	Schema   string   `yaml:"schema"`
	Name     string   `yaml:"name"`
	Version  string   `yaml:"version"`
	Kind     string   `yaml:"kind"`
	Summary  string   `yaml:"summary"`
	Commands []string `yaml:"commands"`
	Effects  Effects  `yaml:"effects"`
	Guards   []string `yaml:"guards,omitempty"`
	// ReadOnlyFor are first arguments that make a call change nothing (tf
	// plan), for a module that isn't read_only as a whole; DestructiveFor
	// are ones that change something, which lint wants behind a guard (tf
	// apply).
	ReadOnlyFor    []string `yaml:"read_only_for,omitempty"`
	DestructiveFor []string `yaml:"destructive_for,omitempty"`
	// Gate: a failure stops the round, as mod --gate does, however the
	// lane calls it (a check that the round must not go on without).
	Gate bool `yaml:"gate,omitempty"`
	// Usage is a line per command, shown by swim modules show.
	Usage []string `yaml:"usage,omitempty"`
}

var (
	nameRE    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	cmdRE     = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	versionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	flagRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ValidName reports whether s is a module name (notify, aws-session).
func ValidName(s string) bool { return nameRE.MatchString(s) }

// ParseSpec reads and checks a module.yml.
func ParseSpec(data []byte) (Spec, error) {
	var s Spec
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("%s: %w", SpecFile, err)
	}
	if s.Schema != Schema {
		return s, fmt.Errorf("%s: schema %q, want %s", SpecFile, s.Schema, Schema)
	}
	if !nameRE.MatchString(s.Name) {
		return s, fmt.Errorf("%s: name %q isn't a module name (lower case letters, digits and -)", SpecFile, s.Name)
	}
	if !versionRE.MatchString(s.Version) {
		return s, fmt.Errorf("%s: version %q isn't MAJOR.MINOR.PATCH", SpecFile, s.Version)
	}
	switch s.Kind {
	case Bash, Go:
	default:
		return s, fmt.Errorf("%s: kind %q, want bash or go", SpecFile, s.Kind)
	}
	if len(s.Commands) == 0 {
		return s, fmt.Errorf("%s: no commands", SpecFile)
	}
	for _, c := range s.Commands {
		if !cmdRE.MatchString(c) {
			return s, fmt.Errorf("%s: command %q isn't a command name", SpecFile, c)
		}
		// Commands are namespaced by the module: notify, notify_thread.
		if c != s.Name && !strings.HasPrefix(c, s.Name+"_") && !strings.HasPrefix(c, s.Name+"-") {
			return s, fmt.Errorf("%s: command %q isn't %s or %s_<something>", SpecFile, c, s.Name, s.Name)
		}
	}
	if s.Kind == Go && (len(s.Commands) != 1 || s.Commands[0] != s.Name) {
		return s, fmt.Errorf("%s: a go module is one program: commands: [%s]", SpecFile, s.Name)
	}
	for _, g := range s.Guards {
		if !flagRE.MatchString(g) {
			return s, fmt.Errorf("%s: guard %q isn't a guard flag", SpecFile, g)
		}
	}
	return s, nil
}

// Major is the version's major number.
func (s Spec) Major() int {
	m, _ := strconv.Atoi(strings.SplitN(s.Version, ".", 2)[0])
	return m
}

// Module is one module, its files read once.
type Module struct {
	Spec
	// Ref is how the lane declared it (notify, clone@1, ./modules/x).
	Ref     string
	Builtin bool
	// Dir is a repo module's directory, repo-relative ("" if built in).
	Dir string
	// Files are its files, relative to its directory.
	Files  map[string][]byte
	SHA256 string // TreeHash(Files)
}

// Source is where the module comes from: builtin, or ./<dir>.
func (m *Module) Source() string {
	if m.Builtin {
		return BuiltinSource
	}
	return "./" + m.Dir
}

// Has reports whether cmd is one of the module's commands.
func (m *Module) Has(cmd string) bool { return slices.Contains(m.Commands, cmd) }

// Script is a bash module's source.
func (m *Module) Script() []byte { return m.Files[m.Name+".sh"] }

// ReadOnlyCall reports whether a call with these arguments changes
// nothing: the module is read_only, or its first argument is in
// read_only_for.
func (m *Module) ReadOnlyCall(args []string) bool {
	return m.Effects.ReadOnly || len(args) > 0 && slices.Contains(m.ReadOnlyFor, args[0])
}

// DestructiveCall reports whether a call with these arguments is one
// module.yml's destructive_for says belongs behind a guard.
func (m *Module) DestructiveCall(args []string) bool {
	return len(args) > 0 && slices.Contains(m.DestructiveFor, args[0])
}

// EffectsText is the module's effects in a line: "credentials, network
// sts.amazonaws.com; secret SLACK_WEBHOOK_URL".
func (m *Module) EffectsText() string {
	var parts []string
	e := m.Effects
	if e.Credentials {
		parts = append(parts, "credentials")
	}
	if len(e.Network) > 0 {
		parts = append(parts, "network "+strings.Join(e.Network, " "))
	}
	if len(e.Secrets) > 0 {
		parts = append(parts, "secret "+strings.Join(e.Secrets, " "))
	}
	if len(e.Writes) > 0 {
		parts = append(parts, "writes "+strings.Join(e.Writes, " "))
	}
	if e.ReadOnly {
		parts = append(parts, "read-only")
	}
	if len(m.ReadOnlyFor) > 0 {
		parts = append(parts, "read-only for "+strings.Join(m.ReadOnlyFor, " "))
	}
	if len(m.DestructiveFor) > 0 {
		parts = append(parts, "guard "+strings.Join(m.DestructiveFor, " "))
	}
	if len(m.Guards) > 0 {
		parts = append(parts, "guards "+strings.Join(m.Guards, " "))
	}
	if m.Gate {
		parts = append(parts, "gate")
	}
	if len(parts) == 0 {
		return "no declared effects"
	}
	return strings.Join(parts, ", ")
}

// TreeHash is the SHA-256 of a module's files: each path and its own
// SHA-256, in path order.
func TreeHash(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	b.WriteString("swim module tree v1\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "%s %s\n", p, Hash(files[p]))
	}
	return Hash([]byte(b.String()))
}

// Hash is a SHA-256 in hex.
func Hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// New makes a module from its files, checking its module.yml and that
// what it says is there is there.
func New(files map[string][]byte) (*Module, error) {
	data, ok := files[SpecFile]
	if !ok {
		return nil, errors.New("no " + SpecFile)
	}
	spec, err := ParseSpec(data)
	if err != nil {
		return nil, err
	}
	m := &Module{Spec: spec, Files: files, SHA256: TreeHash(files)}
	if spec.Kind == Bash {
		src := m.Script()
		if src == nil {
			return nil, fmt.Errorf("a bash module needs %s.sh", spec.Name)
		}
		defined := BashFunctions(src)
		for _, c := range spec.Commands {
			if !slices.Contains(defined, c) {
				return nil, fmt.Errorf("%s.sh doesn't define %s, a command module.yml lists", spec.Name, c)
			}
		}
	}
	return m, nil
}

var funcRE = regexp.MustCompile(`(?m)^\s*(?:function\s+)?([A-Za-z_][A-Za-z0-9_-]*)\s*\(\s*\)`)

// BashFunctions lists the functions a bash file defines at the start of a
// line.
func BashFunctions(src []byte) []string {
	var out []string
	for _, m := range funcRE.FindAllSubmatch(src, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

// Limits on a repo module's tree.
const (
	maxFiles = 200
	maxBytes = 8 << 20
)

// ReadDir reads a module's tree from dir: every file, except hidden ones.
func ReadDir(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	var total int
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file (modules hold files only)", p)
		}
		rel, _ := filepath.Rel(dir, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		total += len(data)
		if len(files) >= maxFiles || total > maxBytes {
			return fmt.Errorf("over %d files or %d MiB: a module is a small utility", maxFiles, maxBytes>>20)
		}
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	return files, err
}

// Ref is a # Modules: entry: a name (with an optional @major), or a repo
// module's ./path.
type Ref struct {
	Text  string // as written
	Name  string // notify ("" for a path)
	Path  string // modules/notify, repo-relative ("" for a name)
	Major int    // -1: any
}

// ParseRef reads a # Modules: entry.
func ParseRef(s string) (Ref, error) {
	r := Ref{Text: s, Major: -1}
	body := s
	if i := strings.LastIndex(s, "@"); i >= 0 {
		maj, err := strconv.Atoi(s[i+1:])
		if err != nil || maj < 0 || strings.HasPrefix(s[i+1:], "+") {
			return r, fmt.Errorf("%q: @ takes a major version (notify@1)", s)
		}
		r.Major, body = maj, s[:i]
	}
	if strings.HasPrefix(body, "./") {
		p := path.Clean(body)
		if p == "." || p == ".." || strings.HasPrefix(p, "../") {
			return r, fmt.Errorf("%q isn't a directory inside the repo", s)
		}
		r.Path = p
		return r, nil
	}
	if !nameRE.MatchString(body) {
		return r, fmt.Errorf("%q isn't a module name or a ./path", s)
	}
	r.Name = body
	return r, nil
}

// Resolve finds the modules a lane declares, in order, as swim.yml's
// policy allows them. Every problem is in the error, one per line.
func Resolve(root string, pol *config.ModulesPolicy, refs []string) ([]*Module, error) {
	var out []*Module
	var errs []error
	for _, r := range refs {
		m, err := Lookup(root, pol, r)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, m)
	}
	// Two modules with one command name: mod couldn't tell them apart.
	seen := map[string]*Module{}
	for _, m := range out {
		if prev := seen[m.Name]; prev != nil {
			errs = append(errs, fmt.Errorf("module %s is declared twice (%s and %s)", m.Name, prev.Ref, m.Ref))
			continue
		}
		seen[m.Name] = m
	}
	cmds := map[string]string{}
	for _, m := range out {
		for _, c := range m.Commands {
			if other, ok := cmds[c]; ok && other != m.Name {
				errs = append(errs, fmt.Errorf("modules %s and %s both have a command %s", other, m.Name, c))
			}
			cmds[c] = m.Name
		}
	}
	return out, errors.Join(errs...)
}

// Lookup resolves one # Modules: entry.
func Lookup(root string, pol *config.ModulesPolicy, ref string) (*Module, error) {
	r, err := ParseRef(ref)
	if err != nil {
		return nil, fmt.Errorf("module %w", err)
	}
	var m *Module
	switch {
	case r.Path != "":
		if m, err = repoModule(root, r.Path); err != nil {
			return nil, fmt.Errorf("module %s: %w", ref, err)
		}
	default:
		b := Builtin(r.Name)
		var local *Module
		if d := pol.ModuleDir(); d != "" {
			dir := path.Join(d, r.Name)
			if _, err := os.Stat(filepath.Join(root, dir, SpecFile)); err == nil {
				if local, err = repoModule(root, dir); err != nil {
					return nil, fmt.Errorf("module %s: %w", ref, err)
				}
			}
		}
		switch {
		case local != nil && b != nil && !pol.Overrides(r.Name):
			return nil, fmt.Errorf("module %s: ./%s would shadow the built-in %s: name it ./%s, or add override: [%s] to swim.yml modules:", ref, local.Dir, r.Name, local.Dir, r.Name)
		case local != nil:
			m = local
		case b != nil:
			m = b
		default:
			if _, err := os.Stat(filepath.Join(root, "modules", r.Name, SpecFile)); err == nil && pol.ModuleDir() == "" {
				return nil, fmt.Errorf("module %s: no such module; ./modules/%s is one: declare it as ./modules/%s, or set dir: modules under swim.yml modules:", ref, r.Name, r.Name)
			}
			return nil, fmt.Errorf("module %s: no such module (swim modules lists them)", ref)
		}
	}
	if r.Name != "" && m.Name != r.Name {
		return nil, fmt.Errorf("module %s: %s/%s names it %s", ref, m.Dir, SpecFile, m.Name)
	}
	if r.Major >= 0 && m.Major() != r.Major {
		return nil, fmt.Errorf("module %s: %s is %s %s, not major %d", ref, m.Name, sourceText(m), m.Version, r.Major)
	}
	if !pol.ModuleAllowed(m.Builtin, m.Name, m.Dir) {
		what := m.Name
		if !m.Builtin {
			what = "./" + m.Dir
		}
		return nil, fmt.Errorf("module %s isn't allowed: add %s to swim.yml modules: allow:", ref, what)
	}
	c := *m
	c.Ref = ref
	return &c, nil
}

func sourceText(m *Module) string {
	if m.Builtin {
		return "built in at"
	}
	return "./" + m.Dir + " at"
}

// repoModule reads the module in dir (repo-relative).
func repoModule(root, dir string) (*Module, error) {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("./%s: %w", dir, errors.Unwrap(err))
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("./%s isn't a directory", dir)
	}
	files, err := ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("./%s: %w", dir, err)
	}
	m, err := New(files)
	if err != nil {
		return nil, fmt.Errorf("./%s: %w", dir, err)
	}
	if m.Kind == Go {
		if _, ok := files["go.mod"]; !ok {
			return nil, fmt.Errorf("./%s: a go module needs go.mod", dir)
		}
	}
	m.Dir = dir
	return m, nil
}

// FromStage is a repo module as a lane's stage holds it: dir (repo-relative)
// under stage, checked against the hash its manifest pinned.
func FromStage(stage, dir, sha string) (*Module, error) {
	files, err := ReadDir(filepath.Join(stage, filepath.FromSlash(dir)))
	if err != nil {
		return nil, fmt.Errorf("./%s in the stage: %w", dir, err)
	}
	m, err := New(files)
	if err != nil {
		return nil, fmt.Errorf("./%s in the stage: %w", dir, err)
	}
	if m.SHA256 != sha {
		return nil, fmt.Errorf("./%s in the stage isn't what the manifest pinned (%s, pinned %s)", dir, m.SHA256[:12], sha[:min(12, len(sha))])
	}
	m.Dir = dir
	return m, nil
}

// ForCommand is the module among mods with command cmd (nil if none).
func ForCommand(mods []*Module, cmd string) *Module {
	for _, m := range mods {
		if m.Has(cmd) {
			return m
		}
	}
	return nil
}

// Available lists every module a lane in root could declare by name or
// path: the built-ins, and the repo modules in swim.yml's modules: dir.
// Problems reading a repo module are returned beside the list.
func Available(root string, pol *config.ModulesPolicy) ([]*Module, []error) {
	out := Builtins()
	var errs []error
	if d := pol.ModuleDir(); d != "" {
		entries, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(d)))
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			dir := path.Join(d, e.Name())
			if _, err := os.Stat(filepath.Join(root, dir, SpecFile)); err != nil {
				continue
			}
			m, err := repoModule(root, dir)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			out = append(out, m)
		}
	}
	return out, errs
}
