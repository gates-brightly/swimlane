package module

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gates-brightly/swimlane/internal/chars"
	"github.com/gates-brightly/swimlane/internal/version"
)

// Fetched modules: a # Modules: entry naming a source address,
//
//	github.com/org/swim-modules//pagerduty@2
//
// is a module in another git repository: its repository (fetched over
// https, or as git's url.<base>.insteadOf rewrites it), the directory in
// it, and a version. swim modules get resolves the version to a tag
// (<dir>/vX.Y.Z, or vX.Y.Z for a module at the repository's root), fetches
// it, and pins it in .swim.lock with the commit and the files' hash; the
// files go to .swim/modules/<hash>/. Run time only reads the lock and that
// copy: nothing is fetched while a lane runs.

// IsAddress reports whether a # Modules: entry (without its @version)
// names a fetched module: a path with a host, not a ./path or a name.
func IsAddress(s string) bool {
	return !strings.HasPrefix(s, "./") && strings.Contains(s, "/")
}

// splitAddress is an address's repository (host/owner/repo) and the
// module's directory in it ("" for the root).
func splitAddress(addr string) (repo, dir string, err error) {
	repo, dir, _ = strings.Cut(addr, "//")
	parts := strings.Split(repo, "/")
	if len(parts) < 3 || !strings.Contains(parts[0], ".") {
		return "", "", fmt.Errorf("%q: a source address is host/owner/repo[//dir] (github.com/org/swim-modules//pagerduty)", addr)
	}
	for _, p := range append(parts, strings.Split(dir, "/")...) {
		if p == "" && dir == "" {
			continue
		}
		if p == "" || p == "." || p == ".." || !chars.All(p, chars.Or(chars.Alnum, chars.In("._-"))) {
			return "", "", fmt.Errorf("%q: a source address is host/owner/repo[//dir], each part letters, digits, '.', '_' and '-'", addr)
		}
	}
	return repo, dir, nil
}

// addressName is the module name an address implies: its directory's last
// element, or the repository's.
func addressName(addr string) string {
	repo, dir, _ := strings.Cut(addr, "//")
	if dir != "" {
		return path.Base(dir)
	}
	return path.Base(repo)
}

// StoreRel is where a fetched module with files' hash sha is kept,
// repo-relative.
func StoreRel(sha string) string { return ".swim/modules/" + sha }

// PinDir is where a pinned module's files are, repo-relative (and so in a
// lane's stage): a repo module's ./dir, or a fetched one's store.
func PinDir(source, sha string) string {
	if d, ok := strings.CutPrefix(source, "./"); ok {
		return d
	}
	return StoreRel(sha)
}

// fetchedModule is the module at address as .swim.lock pins it, from its
// copy in the store, checked against the pinned hash.
func fetchedModule(root, address string) (*Module, error) {
	lock, _, err := version.ReadLock(root)
	if err != nil {
		return nil, err
	}
	pin, ok := lock.Fetched(address)
	if !ok {
		return nil, fmt.Errorf("%s isn't fetched: swim modules get (it pins it in %s)", address, version.LockName)
	}
	dir := StoreRel(pin.SHA256)
	files, err := ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if os.IsNotExist(err) || err == nil && len(files) == 0 {
		return nil, fmt.Errorf("%s %s isn't here (no %s): swim modules get", address, pin.Version, dir)
	}
	if err != nil {
		return nil, err
	}
	m, err := New(files)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", address, err)
	}
	if m.SHA256 != pin.SHA256 {
		return nil, fmt.Errorf("%s: %s isn't what %s pins (%s, pinned %s): swim modules get fetches it again", address, dir, version.LockName, short(m.SHA256), short(pin.SHA256))
	}
	if m.Version != pin.Version {
		return nil, fmt.Errorf("%s: its module.yml says %s, and %s pins %s", address, m.Version, version.LockName, pin.Version)
	}
	m.Dir, m.Address = dir, address
	return m, nil
}

// A Fetch is one fetched module: its address, what the lock now pins and
// what it pinned before (zero if nothing).
type Fetch struct {
	Address string
	Pin     version.FetchedModule
	Was     version.FetchedModule
}

// Get resolves ref (a fetched module's # Modules: entry) to a version,
// fetches it into root's store if it isn't there, and returns its pin. A
// version the lock already pins that ref accepts is kept, unless upgrade:
// then the newest tag ref accepts is taken.
func Get(root string, ref Ref, upgrade bool) (Fetch, error) {
	lock, _, err := version.ReadLock(root)
	if err != nil {
		return Fetch{}, err
	}
	f := Fetch{Address: ref.Address}
	f.Was, _ = lock.Fetched(ref.Address)
	repo, dir, err := splitAddress(ref.Address)
	if err != nil {
		return f, err
	}
	url := "https://" + repo
	prefix := "v"
	if dir != "" {
		prefix = dir + "/v"
	}
	want := f.Was.Version
	if want == "" || upgrade || ref.unmet(&Module{Spec: Spec{Version: want}}) != "" {
		tags, err := lsTags(url, prefix)
		if err != nil {
			return f, err
		}
		want = ""
		for _, v := range tags { // newest first
			if ref.unmet(&Module{Spec: Spec{Version: v}}) == "" {
				want = v
				break
			}
		}
		if want == "" {
			return f, fmt.Errorf("%s: no tag %sX.Y.Z in %s meets %s (tags: %s)", ref.Text, prefix, url, ref.Text, strings.Join(tags, ", "))
		}
	}
	if want == f.Was.Version && f.Was.SHA256 != "" {
		if files, err := ReadDir(filepath.Join(root, filepath.FromSlash(StoreRel(f.Was.SHA256)))); err == nil && len(files) > 0 && TreeHash(files) == f.Was.SHA256 {
			f.Pin = f.Was
			return f, nil // pinned, and here as pinned
		}
		makeWritable(filepath.Join(root, filepath.FromSlash(StoreRel(f.Was.SHA256))))
		os.RemoveAll(filepath.Join(root, filepath.FromSlash(StoreRel(f.Was.SHA256)))) // missing or changed: fetched again
	}
	pin, err := fetchTag(root, url, dir, prefix+want, ref)
	if err != nil {
		return f, err
	}
	if want == f.Was.Version && f.Was.SHA256 != "" && pin.SHA256 != f.Was.SHA256 {
		return f, fmt.Errorf("%s %s: the tag's files (%s) aren't what %s pins (%s): it was moved or rewritten; refusing it", ref.Address, want, short(pin.SHA256), version.LockName, short(f.Was.SHA256))
	}
	f.Pin = pin
	return f, nil
}

// lsTags lists url's tags prefix+X.Y.Z as versions, newest first.
func lsTags(url, prefix string) ([]string, error) {
	out, err := gitEnv(exec.Command("git", "ls-remote", "--tags", "--refs", url), "").Output()
	if err != nil {
		return nil, fmt.Errorf("%s: git ls-remote: %s", url, gitError(err))
	}
	var vs []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if v, ok := strings.CutPrefix(f[1], "refs/tags/"+prefix); ok && validVersion(v) {
			vs = append(vs, v)
		}
	}
	sort.Slice(vs, func(i, j int) bool { return CompareVersions(vs[i], vs[j]) > 0 })
	return vs, nil
}

// fetchTag clones url at tag (shallow), reads the module in dir, checks
// it's the module ref names at the tag's version, and keeps it in root's
// store.
func fetchTag(root, url, dir, tag string, ref Ref) (version.FetchedModule, error) {
	var pin version.FetchedModule
	tmp, err := os.MkdirTemp(filepath.Join(root, ".swim"), "module-fetch-")
	if err != nil {
		return pin, err
	}
	defer os.RemoveAll(tmp)
	// A clone into a scratch directory (removed after), never the repo's
	// own git.
	if out, err := gitEnv(exec.Command("git", "clone", "-q", "--depth", "1", "--branch", tag, url, tmp), "").CombinedOutput(); err != nil {
		return pin, fmt.Errorf("%s: git clone %s: %s", url, tag, strings.TrimSpace(string(out)))
	}
	commit, err := gitEnv(exec.Command("git", "rev-parse", "HEAD"), tmp).Output()
	if err != nil {
		return pin, fmt.Errorf("%s: git rev-parse: %s", url, gitError(err))
	}
	files, err := ReadDir(filepath.Join(tmp, filepath.FromSlash(dir)))
	if err != nil {
		return pin, fmt.Errorf("%s at %s: %w", ref.Address, tag, err)
	}
	m, err := New(files)
	if err != nil {
		return pin, fmt.Errorf("%s at %s: %w", ref.Address, tag, err)
	}
	ver := tag[strings.LastIndex(tag, "v")+1:]
	switch {
	case m.Name != ref.Name:
		return pin, fmt.Errorf("%s at %s: its module.yml names it %s, not %s", ref.Address, tag, m.Name, ref.Name)
	case m.Version != ver:
		return pin, fmt.Errorf("%s at %s: its module.yml says version %s", ref.Address, tag, m.Version)
	case m.Kind == Go && files["go.mod"] == nil:
		return pin, fmt.Errorf("%s at %s: a go module needs go.mod", ref.Address, tag)
	}
	store := filepath.Join(root, filepath.FromSlash(StoreRel(m.SHA256)))
	if have, err := ReadDir(store); err != nil || TreeHash(have) != m.SHA256 {
		staging := store + ".tmp"
		os.RemoveAll(staging)
		for rel, data := range files {
			p := filepath.Join(staging, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return pin, err
			}
			if err := os.WriteFile(p, data, 0o444); err != nil {
				return pin, err
			}
		}
		makeWritable(store)
		os.RemoveAll(store)
		if err := os.Rename(staging, store); err != nil {
			return pin, err
		}
	}
	return version.FetchedModule{Version: ver, Commit: strings.TrimSpace(string(commit)), SHA256: m.SHA256}, nil
}

// gitEnv runs c in dir without prompting for credentials.
func gitEnv(c *exec.Cmd, dir string) *exec.Cmd {
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10")
	return c
}

func gitError(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		if l := strings.TrimSpace(strings.SplitN(string(ee.Stderr), "\n", 2)[0]); l != "" {
			return l
		}
	}
	return err.Error()
}

// makeWritable lets a store copy (its files read-only) be replaced.
func makeWritable(dir string) {
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil {
			os.Chmod(p, 0o755)
		}
		return nil
	})
}
