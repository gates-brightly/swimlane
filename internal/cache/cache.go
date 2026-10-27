// Package cache keeps tool download caches between rounds: a lane that
// declares `# Cache: terraform @infra/.terraform.lock.hcl` gets the tool's
// own cache variable (TF_PLUGIN_CACHE_DIR) pointed at a per-repo,
// per-user directory that outlives the round.
//
// Only download caches the tool checks against a lockfile are presets:
// Terraform's plugin cache (provider checksums in .terraform.lock.hcl),
// npm's (integrity hashes in package-lock.json), pip's (--require-hashes)
// and Go's module cache (go.sum). The lockfile is pinned in the lane's
// execution manifest, so the approval covers exactly what the tool will
// accept from the cache: the cache makes a round faster and never decides
// what code runs. Installed trees (node_modules, .terraform/) and build
// caches (GOCACHE) are left out, because nothing checks them on use.
//
// The layout, under Base (~/.cache/swim, ~/Library/Caches/swim on macOS):
//
//	<slug>/                 one repo (Slug: its name and a hash of its remote)
//	  terraform/            a preset's cache, the tool's to manage
//	  index.yml             when each preset was last used, and by which lanes
//	  .locks/<name>.use     held shared by every lane running with the preset
//	  .locks/<name>.write   held by a step that writes an exclusive preset
//
// Every directory is the user's own, mode 0700: a cache another user can
// write is code they can plant, so Open refuses one.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gates-brightly/swimlane/internal/chars"
)

// Preset is a tool cache swim knows how to point at its directory.
type Preset struct {
	Name string
	// Vars are the variables swim sets to the preset's directory.
	Vars []string
	// Lockfiles are the file names the preset's @ file may have: the file
	// the tool checks what it takes from the cache against.
	Lockfiles []string
	// Checks says what the tool checks the cache against, for messages.
	Checks string
	// Exclusive: the tool's cache isn't safe for two writers at once
	// (Terraform's plugin cache during init), so a step that writes it
	// holds the preset's write lock. Writes reports whether a step's
	// command writes it.
	Exclusive bool
	Writes    func(argv []string) bool
}

var presets = []Preset{
	{Name: "terraform", Vars: []string{"TF_PLUGIN_CACHE_DIR"}, Lockfiles: []string{".terraform.lock.hcl"},
		Checks: "provider checksums in .terraform.lock.hcl", Exclusive: true, Writes: terraformWrites},
	{Name: "npm", Vars: []string{"npm_config_cache"}, Lockfiles: []string{"package-lock.json", "npm-shrinkwrap.json"},
		Checks: "integrity hashes in the lockfile"},
	{Name: "pip", Vars: []string{"PIP_CACHE_DIR"}, Lockfiles: []string{"*.txt", "*.lock", "*.in"},
		Checks: "--hash entries (pip install --require-hashes)"},
	{Name: "go", Vars: []string{"GOMODCACHE"}, Lockfiles: []string{"go.sum"},
		Checks: "go.sum (with GOFLAGS=-mod=readonly)"},
}

// Lookup returns the preset called name.
func Lookup(name string) (Preset, bool) {
	for _, p := range presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// Names lists the presets.
func Names() []string {
	out := make([]string, len(presets))
	for i, p := range presets {
		out[i] = p.Name
	}
	return out
}

// LockfileOK reports whether path's name is one preset p checks against.
func (p Preset) LockfileOK(path string) bool {
	base := filepath.Base(path)
	for _, pat := range p.Lockfiles {
		if ok, _ := filepath.Match(pat, base); ok {
			return true
		}
	}
	return false
}

// terraformWrites: `terraform init` (and `get`) write the plugin cache;
// plan, apply and the rest only read the providers init linked. A
// command run through a shell (bash -c "cd x && terraform init") is
// found by its words.
func terraformWrites(argv []string) bool {
	tf, sub := false, false
	for _, a := range argv {
		for _, w := range strings.FieldsFunc(a, func(r rune) bool {
			return r == ' ' || r == '\t' || r == ';' || r == '&' || r == '|' || r == '(' || r == ')'
		}) {
			switch b := filepath.Base(w); {
			case b == "terraform" || b == "tofu":
				tf = true
			case tf && (w == "init" || w == "get"):
				sub = true
			}
		}
	}
	return tf && sub
}

// Decl is one cache a lane declares: a preset and its lockfiles.
type Decl struct {
	Preset    string
	Lockfiles []string // repo-relative; a snapshot's: the files its key hashes
	// Tree is a snapshot's installed tree (Preset "snap"): a repo-relative
	// directory, saved and restored whole (cache save, cache restore).
	Tree string
}

// String is how a declaration prints: {terraform [infra/.terraform.lock.hcl]},
// {snap:web/node_modules [web/package-lock.json]}.
func (d Decl) String() string {
	kind := d.Preset
	if d.Preset == SnapPreset {
		kind += ":" + d.Tree
	}
	return fmt.Sprintf("{%s %v}", kind, d.Lockfiles)
}

// Label is how messages name it: the preset, or snap:<tree>.
func (d Decl) Label() string {
	if d.Preset == SnapPreset {
		return SnapPreset + ":" + d.Tree
	}
	return d.Preset
}

// Name is the cache's name in the repo's cache directory: the preset, or
// snap.<tree> for a snapshot.
func (d Decl) Name() string {
	if d.Preset == SnapPreset {
		return SnapName(d.Tree)
	}
	return d.Preset
}

// ParseDecl parses a # Cache: value, "terraform @infra/.terraform.lock.hcl
// npm @web/package-lock.json": each @file belongs to the preset before it.
// Problems are what it couldn't use, worded for swim lint (the caller says
// where: "Cache:", "lane.3.yml cache:").
func ParseDecl(v string) (decls []Decl, problems []string) {
	cur := -1 // the preset @files belong to; -1 after one that was refused
	for _, tok := range strings.FieldsFunc(v, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' }) {
		switch {
		case tok == "-" || strings.EqualFold(tok, "none"):
		case strings.HasPrefix(tok, SnapPreset+":"):
			cur = -1
			tree := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(tok, SnapPreset+":")))
			if tree == "." || tree == "" || filepath.IsAbs(tree) || tree == ".." || strings.HasPrefix(tree, "../") || strings.HasPrefix(tree, ".swim") || strings.HasPrefix(tree, ".git") {
				problems = append(problems, fmt.Sprintf("%s: a snapshot names a directory in the repo (snap:node_modules), not %q", tok, tree))
				continue
			}
			dup := false
			for _, d := range decls {
				dup = dup || d.Preset == SnapPreset && d.Tree == tree
			}
			if dup {
				problems = append(problems, fmt.Sprintf("%s is named twice", tok))
				continue
			}
			decls = append(decls, Decl{Preset: SnapPreset, Tree: tree})
			cur = len(decls) - 1
		case strings.HasPrefix(tok, "@"):
			f := filepath.ToSlash(filepath.Clean(strings.TrimPrefix(tok, "@")))
			switch {
			case len(decls) == 0 && cur < 0 && len(problems) == 0:
				problems = append(problems, fmt.Sprintf("%s comes before any preset; name the preset first (npm @package-lock.json)", tok))
			case cur < 0:
				// it belongs to a preset already reported
			case f == "." || filepath.IsAbs(f) || f == ".." || strings.HasPrefix(f, "../"):
				problems = append(problems, fmt.Sprintf("%s isn't a file in the repo", tok))
			default:
				decls[cur].Lockfiles = append(decls[cur].Lockfiles, f)
			}
		default:
			cur = -1
			if _, ok := Lookup(tok); !ok {
				problems = append(problems, fmt.Sprintf("unknown preset %q (presets: %s)", tok, strings.Join(Names(), ", ")))
				continue
			}
			dup := false
			for _, d := range decls {
				dup = dup || d.Preset == tok
			}
			if dup {
				problems = append(problems, fmt.Sprintf("%s is named twice", tok))
				continue
			}
			decls = append(decls, Decl{Preset: tok})
			cur = len(decls) - 1
		}
	}
	for _, d := range decls {
		if d.Preset == SnapPreset {
			if len(d.Lockfiles) == 0 {
				problems = append(problems, fmt.Sprintf("snap:%s needs the files its key hashes (snap:%s @package-lock.json): an installed tree is only as good as what it was installed from", d.Tree, d.Tree))
			}
			continue
		}
		p, _ := Lookup(d.Preset)
		if len(d.Lockfiles) == 0 {
			problems = append(problems, fmt.Sprintf("%s needs its lockfile (%s @path/%s): without one the tool has nothing to check the cache against", d.Preset, d.Preset, p.Lockfiles[0]))
		}
		for _, f := range d.Lockfiles {
			if !p.LockfileOK(f) {
				problems = append(problems, fmt.Sprintf("%s isn't a %s lockfile (%s)", f, d.Preset, strings.Join(p.Lockfiles, ", ")))
			}
		}
	}
	return decls, problems
}

// Files lists every lockfile decls name, for pinning.
func Files(decls []Decl) []string {
	var out []string
	for _, d := range decls {
		out = append(out, d.Lockfiles...)
	}
	return out
}

// Base is where caches live: configured (config cache_dir) when set, else
// the user's cache directory ($XDG_CACHE_HOME/swim, ~/.cache/swim,
// ~/Library/Caches/swim). A directory under /tmp is shared by every user,
// so it gets the user's id as a suffix (/tmp/swim_cache-501).
func Base(configured string) (string, error) {
	if configured != "" {
		dir := expandHome(configured)
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("cache_dir must be an absolute path, got %q", configured)
		}
		dir = filepath.Clean(dir)
		if sharedTmp(dir) {
			dir = fmt.Sprintf("%s-%d", dir, os.Getuid())
		}
		return dir, nil
	}
	d, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no cache directory (set cache_dir: %v)", err)
	}
	return filepath.Join(d, "swim"), nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// sharedTmp reports whether dir is under a directory every user writes.
func sharedTmp(dir string) bool {
	for _, t := range []string{"/tmp", "/private/tmp", "/var/tmp", "/dev/shm"} {
		if dir == t || strings.HasPrefix(dir, t+"/") {
			return true
		}
	}
	return false
}

// Slug names root's cache directory: its base name and 6 hex digits of a
// hash of its origin remote, so worktrees and clones of one repo share a
// cache and two repos called api don't. A repo with no remote hashes its
// root path (moving it starts a cold cache). override (config
// cache_slug) wins.
func Slug(root, override string) (string, error) {
	if override != "" {
		if !chars.All(override, slugChar) || strings.HasPrefix(override, ".") {
			return "", fmt.Errorf("cache_slug %q: use letters, digits, . _ - (not starting with .)", override)
		}
		return override, nil
	}
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

// Repo is one repo's caches.
type Repo struct {
	Dir  string // <base>/<slug>
	Slug string
}

// Open returns root's caches under base, creating the directories (mode
// 0700) and refusing any that are links, aren't the user's, or that other
// users can write.
func Open(base, slug string) (*Repo, error) {
	r := &Repo{Dir: filepath.Join(base, slug), Slug: slug}
	if err := ensureDir(base); err != nil {
		return nil, err
	}
	for _, d := range []string{r.Dir, filepath.Join(r.Dir, ".locks")} {
		if err := ensureDir(d); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Path is cache name's directory (a preset, or snap.<tree>), created if
// missing.
func (r *Repo) Path(name string) (string, error) {
	if !known(name) {
		return "", fmt.Errorf("no cache preset %q (presets: %s)", name, strings.Join(Names(), ", "))
	}
	d := filepath.Join(r.Dir, name)
	return d, ensureDir(d)
}

// ensureDir makes dir (mode 0700) if it's missing, then checks it.
func ensureDir(dir string) error {
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("cache: %w", err)
		}
	}
	return CheckDir(dir)
}

// Env is what a lane running with decls gets: each preset's variables set
// to its directory, SWIM_CACHE_<PRESET> the same, and for go
// GOFLAGS=-mod=readonly added to any GOFLAGS in environ (so a step can't
// rewrite go.sum to match what it downloaded).
func (r *Repo) Env(decls []Decl, environ []string) ([]string, error) {
	var env []string
	for _, d := range decls {
		if d.Preset == SnapPreset {
			continue // restored and saved by the lane (cache restore, cache save)
		}
		p, ok := Lookup(d.Preset)
		if !ok {
			return nil, fmt.Errorf("no cache preset %q", d.Preset)
		}
		dir, err := r.Path(d.Preset)
		if err != nil {
			return nil, err
		}
		for _, v := range p.Vars {
			env = append(env, v+"="+dir)
		}
		env = append(env, "SWIM_CACHE_"+strings.ToUpper(d.Preset)+"="+dir)
		if d.Preset == "go" {
			flags := lookup(environ, "GOFLAGS")
			if !strings.Contains(" "+flags+" ", " -mod=") {
				flags = strings.TrimSpace(flags + " -mod=readonly")
			}
			env = append(env, "GOFLAGS="+flags)
		}
	}
	return env, nil
}

func lookup(environ []string, key string) string {
	v := ""
	for _, kv := range environ {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val // the last one wins, as for exec
		}
	}
	return v
}

// Info describes one preset's cache in a repo.
type Info struct {
	Name     string `yaml:"name"`
	Path     string `yaml:"path"`
	Size     int64  `yaml:"size_bytes"`
	LastUsed string `yaml:"last_used,omitempty"` // UTC RFC 3339
	Lanes    []int  `yaml:"lanes,omitempty"`     // lanes that used it
	InUse    bool   `yaml:"in_use"`              // a running lane holds it
}

// List describes every preset cache the repo has, by name.
func (r *Repo) List() ([]Info, error) {
	idx, err := r.readIndex()
	if err != nil {
		return nil, err
	}
	var out []Info
	names := Names()
	if ents, err := os.ReadDir(r.Dir); err == nil {
		for _, e := range ents {
			if e.IsDir() && strings.HasPrefix(e.Name(), SnapPreset+".") {
				names = append(names, e.Name())
			}
		}
	}
	for _, name := range names {
		dir := filepath.Join(r.Dir, name)
		if _, err := os.Lstat(dir); err != nil {
			continue
		}
		e := idx.Presets[name]
		out = append(out, Info{Name: name, Path: dir, Size: Size(dir), LastUsed: e.LastUsed, Lanes: e.Lanes, InUse: r.InUse(name)})
	}
	return out, nil
}

// Total is the size of every cache the repo has.
func (r *Repo) Total() int64 { return Size(r.Dir) }

// Size is the bytes under dir, counting each file once (links aren't
// followed).
func Size(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// Remove deletes preset name's cache, unless a running lane holds it.
func (r *Repo) Remove(name string) error {
	if !known(name) {
		return fmt.Errorf("no cache preset %q (presets: %s)", name, strings.Join(Names(), ", "))
	}
	unlock, ok, err := r.tryExclusiveUse(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("cache %s is in use by a running lane", name)
	}
	defer unlock()
	if err := makeWritable(filepath.Join(r.Dir, name)); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(r.Dir, name)); err != nil {
		return err
	}
	return r.forget(name)
}

// makeWritable gives the owner write access to every directory under dir:
// Go's module cache is read-only on purpose, and RemoveAll can't empty a
// directory it can't write.
func makeWritable(dir string) error {
	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			if fi, err := d.Info(); err == nil && fi.Mode().Perm()&0o200 == 0 {
				os.Chmod(p, fi.Mode().Perm()|0o700)
			}
		}
		return nil
	})
}

// Evict removes whole preset caches, least recently used first, until the
// repo's caches total at most max bytes; a cache a running lane holds is
// never removed. It returns what it removed.
func (r *Repo) Evict(max int64) ([]string, error) {
	if max <= 0 {
		return nil, nil
	}
	list, err := r.List()
	if err != nil {
		return nil, err
	}
	var total int64
	for _, c := range list {
		total += c.Size
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].LastUsed < list[j].LastUsed })
	var gone []string
	for _, c := range list {
		if total <= max {
			break
		}
		if c.InUse {
			continue
		}
		if err := r.Remove(c.Name); err != nil {
			continue // taken by a lane since List
		}
		total -= c.Size
		gone = append(gone, c.Name)
	}
	return gone, nil
}

// FilesKey is 12 hex digits of a hash of files' contents (repo-relative,
// in order; a missing one hashes as missing): a key that changes when a
// lockfile does, for CI caches.
func FilesKey(root string, files []string) string {
	h := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			data = []byte("(missing)")
		}
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// slugChar is a rune a cache directory's name keeps.
var slugChar = chars.Or(chars.Alnum, chars.In("._-"))
