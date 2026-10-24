// Package manifest is a round's execution manifest: everything swim can
// pin about the operation a lane runs, built by the launcher before the
// lane starts. It names the script and every local file it reads as code,
// the sidecar, how the script runs, the policy part of swim.yml and
// lookup.yml's rules. Its hash, manifest_sha256, is what approvals sign.
//
// The launcher copies the pinned files into the run's stage,
// .swim/runs/<run>/<lane>/ (read-only, same layout as the repo), checks the
// copies again right before exec, and runs the copy: an edit to the working
// tree after that changes nothing in the round.
//
// Some inputs differ from machine to machine, so approvals signed on one
// must not depend on them: the env files' values (personal and git-ignored)
// and the runner binary's path and hash. They are recorded under local:,
// checked before exec, and left out of manifest_sha256. Facts swim can't
// pin at all (remote state, what PATH finds) are Observe: probes, checked
// around each guard (lane.Probe, Observe).
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/module"
	"github.com/gates-brightly/swimlane/internal/runner"
)

// Schema identifies a manifest file.
const Schema = "swim.manifest/v1"

// FileName is the manifest's name in a lane's stage.
const FileName = "manifest.yml"

// Environment variables the launcher gives a lane.
const (
	StageEnv    = "SWIM_STAGE"    // the lane's stage directory
	ManifestEnv = "SWIM_MANIFEST" // its manifest.yml (launcher-owned, read-only)
)

// Where an included file came from.
const (
	FromIncludes = "includes" // # Includes: or a Matrix: @file: part of the script hash too
	FromSource   = "source"   // a bash source line, found by a static scan (manifests before header-only pinning)
	FromImport   = "import"   // a Python or TypeScript import of a local module (likewise)
	FromModule   = "module"   // a file of a repo module the lane declares (# Modules:)
)

// ModulePin is one module a lane declares (# Modules:), pinned: a built-in
// by the hash of its embedded files, a repo module by its tree's hash (its
// files are also staged, as includes from: module).
type ModulePin struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	Source  string `yaml:"source"` // builtin, or ./<dir>
	Kind    string `yaml:"kind"`
	SHA256  string `yaml:"sha256"`
}

// File is one pinned file.
type File struct {
	Path   string `yaml:"path"` // repo-relative
	SHA256 string `yaml:"sha256"`
	From   string `yaml:"from,omitempty"`
	Exec   bool   `yaml:"exec,omitempty"` // staged executable (a compiled or direct lane, a helper it runs)
}

// Policy is the policy part of the config.
type Policy struct {
	SHA256  string   `yaml:"sha256"` // config.PolicyHash: the swim.yml policy keys it sets
	Version int      `yaml:"version"`
	Keys    []string `yaml:"keys"`
	Rules   string   `yaml:"lookup_rules,omitempty"` // lookup.yml's require: rules
	// Set is each policy key swim.yml sets, with its value's hash, so a
	// stale approval can say which changed (not hashed).
	Set map[string]string `yaml:"set,omitempty"`
}

// Pinned is what manifest_sha256 covers.
type Pinned struct {
	Script   File   `yaml:"script"`
	Includes []File `yaml:"includes,omitempty"`
	Sidecar  *File  `yaml:"sidecar,omitempty"`
	// Modules are the modules the lane declares, in its order.
	Modules []ModulePin `yaml:"modules,omitempty"`
	// Runner is how the script runs, as the repo says it: bash, its #!
	// line, a swim.yml runner, direct; "local .ts" for a runner from the
	// operator's own config (which differs per machine).
	Runner string `yaml:"runner"`
	Config Policy `yaml:"config"`
	// Workspace is "live" for a lane that runs in the working tree (#
	// Workspace: live), "" for one that runs in its staged copy.
	Workspace string `yaml:"workspace,omitempty"`
}

// Local is what's pinned for this run on this machine but left out of
// manifest_sha256.
type Local struct {
	Env        string `yaml:"env_sha256,omitempty"` // the lane's env from env files
	RunnerPath string `yaml:"runner_path,omitempty"`
	RunnerSHA  string `yaml:"runner_sha256,omitempty"`
	// Built are the repo Go modules' binaries, built from the stage with
	// this machine's Go: what a module call runs.
	Built []Built `yaml:"built,omitempty"`
}

// Built is a repo Go module's binary as staging built it.
type Built struct {
	Module string `yaml:"module"`
	Go     string `yaml:"go"`     // the Go version that built it
	Path   string `yaml:"path"`   // repo-relative, under .swim/modules/build/
	SHA256 string `yaml:"sha256"` // the binary's
}

// Manifest is one lane's execution manifest.
type Manifest struct {
	Schema string `yaml:"schema"`
	Lane   int    `yaml:"lane"`
	Job    string `yaml:"job,omitempty"`
	Run    string `yaml:"run,omitempty"`
	// ScriptSHA is the script hash approvals, queue entries and the chain
	// have always named (lane.ScriptBytes), from the staged bytes.
	ScriptSHA string `yaml:"script_sha256"`
	Pinned    Pinned `yaml:"pinned"`
	Local     Local  `yaml:"local"`
	// Guards are the flags the script, and the files it pins, declare or
	// use: the only ones its lane may ask the operator to approve, unless
	// DynamicGuards (a guard whose flag is named at run time, which its
	// lane may ask for under any name).
	Guards        []string `yaml:"guards,omitempty"`
	DynamicGuards bool     `yaml:"dynamic_guards,omitempty"`
	SHA256        string   `yaml:"manifest_sha256"`

	// Dir is where the manifest is staged ("" before Stage).
	Dir  string            `yaml:"-"`
	data map[string][]byte // repo-relative path -> the bytes read once at Build
}

// Build reads lane n's files once and pins them. envSHA is the lane's env
// from env files (envfile.Resolved.SHA256), "" if unknown.
func Build(root string, cfg *config.Config, n int, envSHA string) (*Manifest, error) {
	path, files, err := lane.ScriptParts(root, n)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rel, ok := relTo(root, path)
	if !ok {
		return nil, fmt.Errorf("swim %d: %s is outside the repo", n, path)
	}
	m := &Manifest{Schema: Schema, Lane: n, data: map[string][]byte{rel: data}}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	m.Pinned.Script = File{Path: rel, SHA256: hash(data), Exec: st.Mode()&0o111 != 0}
	read := func(f string) ([]byte, error) {
		if d, ok := m.data[f]; ok {
			return d, nil
		}
		d, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			return nil, err
		}
		m.data[f] = d
		return d, nil
	}
	pinned := map[string]bool{rel: true}
	for _, f := range files {
		d, err := read(f)
		if err != nil {
			// ScriptBytes hashes a missing @file as "(missing)"; nothing to stage.
			continue
		}
		pinned[f] = true
		// Staged executable as it is in the tree (a helper script the lane
		// runs); like the script's, the mode isn't in the hash.
		exec := false
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			exec = st.Mode()&0o111 != 0
		}
		m.Pinned.Includes = append(m.Pinned.Includes, File{Path: f, SHA256: hash(d), From: FromIncludes, Exec: exec})
	}
	m.ScriptSHA = hash(lane.JoinScript(data, files, read))
	// What it pins is what its header names (# Includes:, # Cache:
	// lockfiles, # Modules:): nothing is found by reading its body.
	// A repo file it sources without naming it is refused at run time.
	info, _ := lane.ReadScript(root, n)
	// The modules it declares, as swim.yml allows them: a module that
	// doesn't resolve keeps the lane from starting.
	mods, err := module.Resolve(root, cfg.Modules, info.Modules)
	if err != nil {
		return nil, fmt.Errorf("swim %d: %w", n, err)
	}
	for _, mod := range mods {
		m.Pinned.Modules = append(m.Pinned.Modules, ModulePin{Name: mod.Name, Version: mod.Version, Source: mod.Source(), Kind: mod.Kind, SHA256: mod.SHA256})
		if mod.Builtin {
			continue
		}
		for rel, d := range mod.Files {
			f := mod.Dir + "/" + rel
			if pinned[f] {
				continue
			}
			pinned[f] = true
			m.data[f] = d
			m.Pinned.Includes = append(m.Pinned.Includes, File{Path: f, SHA256: hash(d), From: FromModule})
		}
	}
	sort.Slice(m.Pinned.Includes, func(i, j int) bool { return m.Pinned.Includes[i].Path < m.Pinned.Includes[j].Path })
	if sc := lane.Sidecar(root, n); isFile(sc) {
		d, err := os.ReadFile(sc)
		if err != nil {
			return nil, err
		}
		srel, _ := relTo(root, sc)
		m.data[srel] = d
		m.Pinned.Sidecar = &File{Path: srel, SHA256: hash(d)}
	}
	plan, perr := runner.Resolve(cfg, path)
	m.Pinned.Runner = runnerPin(plan, perr, path)
	if perr == nil {
		m.Local.RunnerPath, m.Local.RunnerSHA = runnerBinary(plan)
	}
	// A lookup.yml that doesn't parse leaves no rules to pin: approvals
	// bound to the manifest then don't match (the guard says why), but the
	// lane still runs.
	rules := "unreadable lookup.yml"
	if lk, err := lookup.Load(root); err == nil {
		rules = lk.RulesHash()
	}
	set := cfg.PolicySet()
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	m.Pinned.Config = Policy{SHA256: cfg.PolicyHash(), Version: config.PolicyVersion, Keys: keys, Rules: rules, Set: set}
	m.Local.Env = envSHA
	m.Pinned.Workspace = info.Workspace
	m.Job = info.Job
	seen := map[string]bool{}
	for _, g := range append(info.GuardFlags(), lane.ParseScript(string(data)).GuardFlags()...) {
		if !seen[g] {
			seen[g] = true
			m.Guards = append(m.Guards, g)
		}
	}
	// The guards it may ask for are the ones its header (or sidecar)
	// declares, and any at all with "# Guards: *": a guard the body uses
	// without declaring it is refused when it asks.
	m.DynamicGuards = info.DynamicGuards > 0 || lane.ParseScript(string(data)).DynamicGuards > 0
	sort.Strings(m.Guards)
	m.SHA256 = m.Pinned.Hash(n)
	return m, nil
}

// Matches reports whether pin, a manifest_sha256 an approval, queue entry,
// schedule or hub command recorded, pins this manifest.
func (m *Manifest) Matches(pin string) bool {
	return pin != "" && pin == m.SHA256
}

// Hash is manifest_sha256: the SHA-256 of the lane number and the pinned
// entries, in a fixed text form (docs/formats.md). (The job id is in the
// script or sidecar.)
func (p Pinned) Hash(n int) string {
	return hash([]byte(p.text("swim manifest v2", n, "config: "+p.Config.SHA256)))
}

// text is the hashed form of the pins: head, then the lane, script,
// includes, sidecar, modules, runner, config line and lookup rules.
func (p Pinned) text(head string, n int, config string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nlane: %d\nscript: %s %s\n", head, n, p.Script.Path, p.Script.SHA256)
	for _, f := range p.Includes {
		fmt.Fprintf(&b, "include: %s %s\n", f.Path, f.SHA256)
	}
	if p.Sidecar != nil {
		fmt.Fprintf(&b, "sidecar: %s %s\n", p.Sidecar.Path, p.Sidecar.SHA256)
	}
	for _, mod := range p.Modules {
		fmt.Fprintf(&b, "module: %s %s %s %s %s\n", mod.Name, mod.Version, mod.Source, mod.Kind, mod.SHA256)
	}
	fmt.Fprintf(&b, "runner: %s\n%s\nlookup-rules: %s\n", p.Runner, config, p.Config.Rules)
	// Only when live, so a staged lane's hash reads as it always has.
	if p.Workspace != "" {
		fmt.Fprintf(&b, "workspace: %s\n", p.Workspace)
	}
	return b.String()
}

// Stage copies the pinned files into the run's stage for the lane,
// read-only, writes the manifest, and returns the stage directory. A stage left by an earlier attempt of the
// same run is replaced.
func (m *Manifest) Stage(root, run string) (string, error) {
	if m.data == nil {
		return "", errors.New("manifest: only a built manifest can be staged")
	}
	m.Run = run
	dir := Dir(root, run, m.Lane)
	if _, err := os.Stat(dir); err == nil {
		if err := Remove(dir); err != nil {
			return "", err
		}
	}
	write := func(rel string, data []byte, exec bool) error {
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o400)
		if exec {
			mode = 0o500
		}
		return os.WriteFile(dst, data, mode)
	}
	files := append([]File{m.Pinned.Script}, m.Pinned.Includes...)
	if m.Pinned.Sidecar != nil {
		files = append(files, *m.Pinned.Sidecar)
	}
	for _, f := range files {
		if err := write(f.Path, m.data[f.Path], f.Exec); err != nil {
			return "", err
		}
	}
	// Repo Go modules are built now, from the stage: a module that doesn't
	// build keeps the lane from starting, and the binary is in the
	// manifest.
	m.Local.Built = nil
	for _, p := range m.Pinned.Modules {
		if p.Kind != module.Go || p.Source == module.BuiltinSource {
			continue
		}
		rel := module.PinDir(p.Source, p.SHA256)
		mod, err := module.FromStage(dir, rel, p.SHA256)
		if err != nil {
			return "", fmt.Errorf("module %s: %w", p.Name, err)
		}
		bin, gov, err := module.BuildGo(root, mod, filepath.Join(dir, filepath.FromSlash(rel)), os.Stderr)
		if err != nil {
			return "", err
		}
		binRel, _ := relTo(root, bin)
		m.Local.Built = append(m.Local.Built, Built{Module: p.Name, Go: gov, Path: binRel, SHA256: fileHash(bin)})
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	if err := write(FileName, data, false); err != nil {
		return "", err
	}
	m.Dir = dir
	return dir, nil
}

// Dir is lane n's stage in run.
func Dir(root, run string, n int) string {
	return filepath.Join(root, ".swim", "runs", run, strconv.Itoa(n))
}

// RunsDir holds every run's stages (next to the run records).
func RunsDir(root string) string { return filepath.Join(root, ".swim", "runs") }

// Remove deletes a stage (or a run's stages): its files are read-only.
func Remove(dir string) error {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(dir)
}

// Path is the staged path of a repo-relative file.
func (m *Manifest) Path(rel string) string {
	return filepath.Join(m.Dir, filepath.FromSlash(rel))
}

// Workspace is the directory the lane runs in: its stage, or the repo
// (root) for # Workspace: live.
func (m *Manifest) Workspace(root string) string {
	if m.Pinned.Workspace == lane.WorkspaceLive || m.Dir == "" {
		return root
	}
	return m.Dir
}

// Script is the staged script's path.
func (m *Manifest) Script() string { return m.Path(m.Pinned.Script.Path) }

// Load reads the manifest staged in dir.
func Load(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, FileName), err)
	}
	if m.Schema != Schema {
		return nil, fmt.Errorf("%s: not a swim manifest (schema %q)", filepath.Join(dir, FileName), m.Schema)
	}
	m.Dir = dir
	return &m, nil
}

// FromEnv is the manifest the launcher staged for this lane process
// (SWIM_MANIFEST), if it names lane n; nil otherwise (a lane run directly
// with bash, or an older launcher).
func FromEnv(n int) *Manifest {
	p := os.Getenv(ManifestEnv)
	if p == "" {
		return nil
	}
	m, err := Load(filepath.Dir(p))
	if err != nil || m.Lane != n {
		return nil
	}
	return m
}

// Verify checks a staged manifest: its hash still matches its pinned
// entries, and every staged file still matches its pin. The error names
// what changed.
func (m *Manifest) Verify() error {
	var changed []string
	if m.Pinned.Hash(m.Lane) != m.SHA256 {
		changed = append(changed, FileName)
	}
	check := func(what string, f File) {
		data, err := os.ReadFile(m.Path(f.Path))
		if err != nil || hash(data) != f.SHA256 {
			changed = append(changed, what+" "+f.Path)
		}
	}
	check("script", m.Pinned.Script)
	for _, f := range m.Pinned.Includes {
		check("includes", f)
	}
	if m.Pinned.Sidecar != nil {
		check("sidecar", *m.Pinned.Sidecar)
	}
	if len(changed) > 0 {
		return fmt.Errorf("manifest changed since staged (%s)", strings.Join(changed, ", "))
	}
	return nil
}

// ScriptBytes is lane.ScriptBytes of the staged copy: what the chain keeps
// as the round's script.
func (m *Manifest) ScriptBytes() ([]byte, error) {
	data, err := os.ReadFile(m.Script())
	if err != nil || lane.IsBinary(data) {
		return data, err
	}
	var files []string
	for _, f := range m.Pinned.Includes {
		if f.From == FromIncludes {
			files = append(files, f.Path)
		}
	}
	return lane.JoinScript(data, files, func(f string) ([]byte, error) { return os.ReadFile(m.Path(f)) }), nil
}

// Diff says why new differs from old, an approval's pins: "config policy
// changed (swim.yml)", "includes lib/orders.sh changed", ...
func Diff(old, new Pinned) []string {
	var out []string
	if old.Script.Path != new.Script.Path || old.Script.SHA256 != new.Script.SHA256 {
		out = append(out, "script "+new.Script.Path+" changed")
	}
	was := map[string]string{}
	for _, f := range old.Includes {
		was[f.Path] = f.SHA256
	}
	now := map[string]bool{}
	for _, f := range new.Includes {
		now[f.Path] = true
		switch sum, ok := was[f.Path]; {
		case !ok:
			out = append(out, "includes "+f.Path+" added")
		case sum != f.SHA256:
			out = append(out, "includes "+f.Path+" changed")
		}
	}
	for _, f := range old.Includes {
		if !now[f.Path] {
			out = append(out, "includes "+f.Path+" removed")
		}
	}
	switch {
	case old.Sidecar == nil && new.Sidecar != nil:
		out = append(out, "sidecar "+new.Sidecar.Path+" added")
	case old.Sidecar != nil && new.Sidecar == nil:
		out = append(out, "sidecar "+old.Sidecar.Path+" removed")
	case old.Sidecar != nil && *old.Sidecar != *new.Sidecar:
		out = append(out, "sidecar "+new.Sidecar.Path+" changed")
	}
	out = append(out, diffModules(old.Modules, new.Modules)...)
	if old.Runner != new.Runner {
		out = append(out, fmt.Sprintf("runner changed (%s, now %s)", or(old.Runner, "none"), or(new.Runner, "none")))
	}
	out = append(out, diffPolicy(old.Config, new.Config)...)
	if old.Config.Rules != new.Config.Rules {
		out = append(out, "approval rules changed (lookup.yml require:)")
	}
	return out
}

// diffPolicy says how swim.yml's policy changed between an approval's pins
// and now, naming the keys when both sides recorded them.
func diffPolicy(old, new Policy) []string {
	if old.SHA256 == new.SHA256 {
		return nil
	}
	var keys []string
	for k, sum := range new.Set {
		if old.Set[k] != sum {
			keys = append(keys, k)
		}
	}
	for k := range old.Set {
		if _, ok := new.Set[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return []string{"config policy changed (swim.yml)"}
	}
	return []string{"config policy changed (swim.yml: " + strings.Join(keys, ", ") + ")"}
}

// diffModules says how a lane's modules changed: "module notify added",
// "module notify changed (1.0.0, now 1.1.0)".
func diffModules(old, new []ModulePin) []string {
	var out []string
	was := map[string]ModulePin{}
	for _, p := range old {
		was[p.Name] = p
	}
	now := map[string]bool{}
	for _, p := range new {
		now[p.Name] = true
		switch o, ok := was[p.Name]; {
		case !ok:
			out = append(out, "module "+p.Name+" added")
		case o.Version != p.Version:
			out = append(out, fmt.Sprintf("module %s changed (%s, now %s)", p.Name, o.Version, p.Version))
		case o.Source != p.Source:
			out = append(out, fmt.Sprintf("module %s changed (%s, now %s)", p.Name, o.Source, p.Source))
		case o != p:
			out = append(out, "module "+p.Name+" changed")
		}
	}
	for _, p := range old {
		if !now[p.Name] {
			out = append(out, "module "+p.Name+" removed")
		}
	}
	return out
}

// runnerPin is how the repo says the script runs, the same on every
// machine.
func runnerPin(p runner.Plan, err error, script string) string {
	if err != nil {
		var need *runner.NeedRunner
		if errors.As(err, &need) {
			return "local ." + need.Ext
		}
		return "none"
	}
	words := p.Argv[:len(p.Argv)-1]
	switch p.Source {
	case runner.Direct:
		return "direct"
	case runner.Builtin:
		return strings.Join(words, " ")
	case runner.Shebang:
		return "#!" + strings.Join(words, " ")
	case runner.RepoYML:
		return "swim.yml " + strings.Join(words, " ")
	}
	return "local ." + strings.ToLower(strings.TrimPrefix(filepath.Ext(script), "."))
}

var (
	binMu    sync.Mutex
	binCache = map[string][2]string{} // path|size|mtime -> path, sha
)

// runnerBinary is the runner's binary on this machine and its hash
// (recorded, not part of manifest_sha256: an OS update would otherwise stale
// every approval).
func runnerBinary(p runner.Plan) (string, string) {
	if p.Source == runner.Direct || len(p.Argv) < 2 {
		return "", ""
	}
	cmd := p.Argv[0]
	if filepath.Base(cmd) == "env" && len(p.Argv) > 2 {
		cmd = p.Argv[1] // #!/usr/bin/env python3
	}
	path, err := exec.LookPath(cmd)
	if err != nil {
		return "", ""
	}
	return path, fileHash(path)
}

// fileHash is a file's SHA-256, cached while its size and time stay the
// same (a runner binary is hashed once per process).
func fileHash(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	key := fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
	binMu.Lock()
	c, ok := binCache[key]
	binMu.Unlock()
	if ok {
		return c[1]
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	sum := hex.EncodeToString(h.Sum(nil))
	binMu.Lock()
	binCache[key] = [2]string{path, sum}
	binMu.Unlock()
	return sum
}

func hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func relTo(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// OldStages lists the runs whose stages (.swim/runs/<run>/) are older than
// keep, leaving out those keepRun keeps (a lane's latest round).
func OldStages(root string, keep time.Duration, keepRun func(run string) bool) []string {
	entries, _ := os.ReadDir(RunsDir(root))
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err == nil && time.Since(info.ModTime()) >= keep && !keepRun(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

// PruneStages removes what OldStages lists and returns the runs removed.
// Their run records (.swim/runs/<run>.yml) stay, for swim timeline.
func PruneStages(root string, keep time.Duration, keepRun func(run string) bool) []string {
	var removed []string
	for _, run := range OldStages(root, keep, keepRun) {
		if Remove(filepath.Join(RunsDir(root), run)) == nil {
			removed = append(removed, run)
		}
	}
	return removed
}
