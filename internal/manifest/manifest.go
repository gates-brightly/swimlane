// Package manifest is a round's execution manifest: everything swim can
// pin about the operation a lane runs, built by the launcher before the
// lane starts. It names the script and every local file it reads as code,
// the sidecar and how the script runs. Its
// hash, manifest_sha256, is what a round's chain entry and a queued batch
// pin.
//
// The launcher copies the pinned files into the run's stage,
// .swim/runs/<run>/<lane>/ (read-only, same layout as the repo), checks the
// copies again right before exec, and runs the copy: an edit to the working
// tree after that changes nothing in the round.
//
// Some inputs differ from machine to machine, so approvals signed on one
// must not depend on them: the env files' values (personal and git-ignored)
// and the runner binary's path and hash. They are recorded under local:,
// checked before exec, and left out of manifest_sha256.
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
	"github.com/gates-brightly/swimlane/internal/runner"
)

// Schema identifies a manifest file.
const Schema = "swim.manifest/v1"

// FileName is the manifest's name: <stage>.manifest.yml, beside the
// lane's stage (.swim/runs/<run>/<N>.manifest.yml), never in it, so the
// lane's working directory holds only the files it pinned.
const FileName = "manifest.yml"

// FilePath is the manifest of the stage in dir.
func FilePath(dir string) string { return filepath.Clean(dir) + "." + FileName }

// Environment variables the launcher gives a lane.
const (
	StageEnv    = "SWIM_STAGE"    // the lane's stage directory
	ManifestEnv = "SWIM_MANIFEST" // its manifest (launcher-owned, read-only)
)

// Where an included file came from.
const (
	FromIncludes = "includes" // # Includes: (or an older swim's Matrix: @file:) part of the script hash too
	FromSource   = "source"   // a bash source line, found by a static scan (manifests before header-only pinning)
	FromImport   = "import"   // a Python or TypeScript import of a local module (likewise)
)

// File is one pinned file.
type File struct {
	Path   string `yaml:"path"` // repo-relative
	SHA256 string `yaml:"sha256"`
	From   string `yaml:"from,omitempty"`
	Exec   bool   `yaml:"exec,omitempty"` // staged executable (a compiled or direct lane, a helper it runs)
}

// Policy is the policy part of the config: the policy keys a repo's
// committed swim.yml set, when swim read one. Every manifest swim writes
// now has noPolicy; older ones keep what they recorded.
type Policy struct {
	SHA256  string   `yaml:"sha256"` // the policy hash (docs/reference/formats.md)
	Version int      `yaml:"version"`
	Keys    []string `yaml:"keys"`
	// Rules were lookup.yml's require: rules when swim had signed
	// approvals: no longer set, but hashed as written (an empty line).
	Rules string `yaml:"lookup_rules,omitempty"`
	// Set was each policy key swim.yml set, with its value's hash: a
	// record of what the policy was (not hashed). legacy: no longer written.
	Set map[string]string `yaml:"set,omitempty"`
}

// Pinned is what manifest_sha256 covers.
type Pinned struct {
	Script   File   `yaml:"script"`
	Includes []File `yaml:"includes,omitempty"`
	Sidecar  *File  `yaml:"sidecar,omitempty"`
	// Runner is how the script runs, as the repo says it: bash, its #!
	// line, direct; "local .ts" for a runner from the
	// operator's own config (which differs per machine).
	Runner string `yaml:"runner"`
	Config Policy `yaml:"config"`
	// Workspace is "live" for a lane that runs in the working tree (#
	// Workspace: live), "" for one that runs in its pinned copy.
	Workspace string `yaml:"workspace,omitempty"`
}

// Local is what's pinned for this run on this machine but left out of
// manifest_sha256.
type Local struct {
	Env        string `yaml:"env_sha256,omitempty"` // the lane's env from env files
	RunnerPath string `yaml:"runner_path,omitempty"`
	RunnerSHA  string `yaml:"runner_sha256,omitempty"`
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
		return nil, fmt.Errorf("lane %d: %s is outside the repo", n, path)
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
	for _, f := range files {
		d, err := read(f)
		if err != nil {
			// ScriptBytes hashes a missing @file as "(missing)"; nothing to stage.
			continue
		}
		// Staged executable as it is in the tree (a helper script the lane
		// runs); like the script's, the mode isn't in the hash.
		exec := false
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			exec = st.Mode()&0o111 != 0
		}
		m.Pinned.Includes = append(m.Pinned.Includes, File{Path: f, SHA256: hash(d), From: FromIncludes, Exec: exec})
	}
	m.ScriptSHA = hash(lane.JoinScript(data, files, read))
	// What it pins is what its header names (# Includes:): nothing is
	// found by reading its body. A repo file it
	// sources without naming it is refused at run time.
	info, _ := lane.ReadScript(root, n)
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
	m.Pinned.Config = noPolicy
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

// noPolicy is the config part every manifest pins now: the policy hash of
// no keys set, in its frozen form, so a manifest_sha256 reads as it did
// for a repo whose swim.yml (now gone) set no policy key.
var noPolicy = Policy{SHA256: hash([]byte("swim policy v2\n")), Version: 3, Keys: []string{}}

// Matches reports whether pin, a manifest_sha256 an approval or queue
// entry recorded, pins this manifest.
func (m *Manifest) Matches(pin string) bool {
	return pin != "" && pin == m.SHA256
}

// Hash is manifest_sha256: the SHA-256 of the lane number and the pinned
// entries, in a fixed text form (docs/reference/formats.md). (The job id is in the
// script or sidecar.)
func (p Pinned) Hash(n int) string {
	return hash([]byte(p.text("swim manifest v2", n, "config: "+p.Config.SHA256)))
}

// hashV1 is the manifest v1 hash of the pins (swim before 0b9aba3,
// 2026-10-24): what a manifest staged by an older swim recorded. Frozen:
// swim verify rechecks those pinned copies with it.
func (p Pinned) hashV1(n int) string {
	return hash([]byte(p.text("swim manifest v1", n, fmt.Sprintf("config: v%d %s", p.Config.Version, p.Config.SHA256))))
}

// text is the hashed form of the pins: head, then the lane, script,
// includes, sidecar, runner, config line and lookup rules.
func (p Pinned) text(head string, n int, config string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nlane: %d\nscript: %s %s\n", head, n, p.Script.Path, p.Script.SHA256)
	for _, f := range p.Includes {
		fmt.Fprintf(&b, "include: %s %s\n", f.Path, f.SHA256)
	}
	if p.Sidecar != nil {
		fmt.Fprintf(&b, "sidecar: %s %s\n", p.Sidecar.Path, p.Sidecar.SHA256)
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
	if err := Remove(dir); err != nil {
		return "", err
	}
	write := func(dst string, data []byte, exec bool) error {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o400)
		if exec {
			mode = 0o500
		}
		return os.WriteFile(dst, data, mode)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, f := range m.Files() {
		if err := write(filepath.Join(dir, filepath.FromSlash(f.Path)), m.data[f.Path], f.Exec); err != nil {
			return "", err
		}
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	if err := write(FilePath(dir), data, false); err != nil {
		return "", err
	}
	m.Dir = dir
	return dir, nil
}

// Dir is lane n's stage in run.
func Dir(root, run string, n int) string {
	return filepath.Join(root, ".swim", "runs", run, strconv.Itoa(n))
}

// RunsDir holds every run's stages.
func RunsDir(root string) string { return filepath.Join(root, ".swim", "runs") }

// Remove deletes a stage and its manifest (or a run's stages): its files
// are read-only.
func Remove(dir string) error {
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			os.Chmod(p, 0o700)
		}
		return nil
	})
	if mf := FilePath(dir); isFile(mf) {
		os.Chmod(mf, 0o600)
		os.Remove(mf)
	}
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

// Files are the pinned files: the script, its includes and its sidecar.
func (m *Manifest) Files() []File {
	files := append([]File{m.Pinned.Script}, m.Pinned.Includes...)
	if m.Pinned.Sidecar != nil {
		files = append(files, *m.Pinned.Sidecar)
	}
	return files
}

// Bytes is a pinned file's content: as Build read it, else its staged
// copy.
func (m *Manifest) Bytes(rel string) ([]byte, error) {
	if d, ok := m.data[rel]; ok {
		return d, nil
	}
	if m.Dir == "" {
		return nil, fmt.Errorf("manifest: %s isn't staged", rel)
	}
	return os.ReadFile(m.Path(rel))
}

// Load reads the manifest staged in dir.
func Load(dir string) (*Manifest, error) {
	path := FilePath(dir)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		// A stage from an older swim keeps its manifest inside it.
		if d, err2 := os.ReadFile(filepath.Join(dir, FileName)); err2 == nil {
			path, data, err = filepath.Join(dir, FileName), d, nil
		}
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m.Schema != Schema {
		return nil, fmt.Errorf("%s: not a swim manifest (schema %q)", path, m.Schema)
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
	dir, ok := strings.CutSuffix(p, "."+FileName)
	if !ok {
		return nil
	}
	m, err := Load(dir)
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
	if m.Pinned.Hash(m.Lane) != m.SHA256 && m.Pinned.hashV1(m.Lane) != m.SHA256 {
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

// ScriptBytes is lane.ScriptBytes of the pinned copy: what the chain keeps
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
	case runner.Short:
		// swim's prelude, like its library, comes with the swim that runs it.
		return "bash (short form)"
	case runner.Shebang:
		return "#!" + strings.Join(words, " ")
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
func PruneStages(root string, keep time.Duration, keepRun func(run string) bool) []string {
	var removed []string
	for _, run := range OldStages(root, keep, keepRun) {
		if Remove(filepath.Join(RunsDir(root), run)) == nil {
			removed = append(removed, run)
		}
	}
	return removed
}
