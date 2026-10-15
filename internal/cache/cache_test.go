package cache

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseDecl(t *testing.T) {
	decls, problems := ParseDecl("npm @web/package-lock.json, terraform @infra/orders/.terraform.lock.hcl @infra/dns/.terraform.lock.hcl")
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	want := []Decl{
		{Preset: "npm", Lockfiles: []string{"web/package-lock.json"}},
		{Preset: "terraform", Lockfiles: []string{"infra/orders/.terraform.lock.hcl", "infra/dns/.terraform.lock.hcl"}},
	}
	if fmt.Sprint(decls) != fmt.Sprint(want) {
		t.Errorf("decls = %v, want %v", decls, want)
	}
	if got := Files(decls); len(got) != 3 {
		t.Errorf("Files = %v", got)
	}
	for v, want := range map[string]string{
		"terraform":                       "needs its lockfile",
		"@go.sum go":                      "comes before any preset",
		"maven @pom.xml":                  `unknown preset "maven"`,
		"npm @package.json":               "isn't a npm lockfile",
		"npm @../other/package-lock.json": "isn't a file in the repo",
		"go @go.sum go @x/go.sum":         "named twice",
	} {
		_, problems := ParseDecl(v)
		if !strings.Contains(strings.Join(problems, "; "), want) {
			t.Errorf("ParseDecl(%q) problems = %v, want %q", v, problems, want)
		}
	}
	if decls, problems := ParseDecl("none"); len(decls)+len(problems) != 0 {
		t.Errorf("none: %v %v", decls, problems)
	}
}

func TestTerraformWrites(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"terraform", "-chdir=infra", "init", "-input=false"}, true},
		{[]string{"/usr/local/bin/tofu", "init"}, true},
		{[]string{"bash", "-c", "cd infra && terraform init -upgrade"}, true},
		{[]string{"terraform", "get"}, true},
		{[]string{"terraform", "plan"}, false},
		{[]string{"terraform", "apply", "-target=module.init"}, false},
		{[]string{"npm", "init"}, false},
	} {
		if got := terraformWrites(tc.argv); got != tc.want {
			t.Errorf("terraformWrites(%q) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}

func TestBase(t *testing.T) {
	uid := os.Getuid()
	for in, want := range map[string]string{
		"/tmp/swim_cache": fmt.Sprintf("/tmp/swim_cache-%d", uid),
		"/private/tmp/x/": fmt.Sprintf("/private/tmp/x-%d", uid),
		"/var/cache/swim": "/var/cache/swim",
		"/tmpfoo/swim":    "/tmpfoo/swim",
	} {
		got, err := Base(in)
		if err != nil || got != want {
			t.Errorf("Base(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := Base("relative/dir"); err == nil {
		t.Error("a relative cache_dir was accepted")
	}
	t.Setenv("XDG_CACHE_HOME", "/xdg")
	if got, _ := Base(""); got != filepath.Join(must(os.UserCacheDir()), "swim") {
		t.Errorf("Base default = %q", got)
	}
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}

func TestSlug(t *testing.T) {
	remotes := map[string]string{}
	old := remoteFn
	remoteFn = func(root string) string { return remotes[root] }
	t.Cleanup(func() { remoteFn = old })

	remotes["/a/api"] = "git@github.com:org/api.git"
	remotes["/b/api-wt"] = "git@github.com:org/api" // a worktree: same remote, .git or not
	remotes["/c/api"] = "git@github.com:other/api.git"
	a, _ := Slug("/a/api", "")
	wt, _ := Slug("/b/api-wt", "")
	c, _ := Slug("/c/api", "")
	if !strings.HasPrefix(a, "api-") || len(a) != len("api-")+6 {
		t.Errorf("slug %q isn't name-<6 hex>", a)
	}
	if a[len(a)-6:] != wt[len(wt)-6:] {
		t.Errorf("worktree of the same remote: %q vs %q", a, wt)
	}
	if a == c {
		t.Errorf("two repos called api share %q", a)
	}
	// No remote: the root path, so a moved checkout changes it.
	x, _ := Slug("/d/tool", "")
	y, _ := Slug("/e/tool", "")
	if x == y {
		t.Errorf("no remote: two paths share %q", x)
	}
	if s, err := Slug("/d/tool", "shared-tool"); err != nil || s != "shared-tool" {
		t.Errorf("override: %q %v", s, err)
	}
	for _, bad := range []string{"a/b", "..", ".hidden", "sp ace"} {
		if _, err := Slug("/d/tool", bad); err == nil {
			t.Errorf("cache_slug %q accepted", bad)
		}
	}
}

func TestOpenRefusesUnsafeDirs(t *testing.T) {
	base := filepath.Join(t.TempDir(), "swim")
	r, err := Open(base, "repo-abc123")
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(r.Dir)
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("repo dir mode %o, want 700", fi.Mode().Perm())
	}
	// Writable by others.
	os.Chmod(r.Dir, 0o777)
	if _, err := Open(base, "repo-abc123"); err == nil || !strings.Contains(err.Error(), "writable by others") {
		t.Errorf("mode 777 accepted: %v", err)
	}
	os.Chmod(r.Dir, 0o700)
	// A symlinked preset directory.
	elsewhere := t.TempDir()
	os.Symlink(elsewhere, filepath.Join(r.Dir, "npm"))
	if _, err := r.Path("npm"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("symlinked preset dir accepted: %v", err)
	}
	if _, err := r.Path("maven"); err == nil {
		t.Error("unknown preset accepted")
	}
}

func TestEnv(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "swim"), "repo-abc123")
	if err != nil {
		t.Fatal(err)
	}
	decls, _ := ParseDecl("terraform @.terraform.lock.hcl go @go.sum")
	env, err := r.Env(decls, []string{"GOFLAGS=-trimpath"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(env, "\n")
	for _, want := range []string{
		"TF_PLUGIN_CACHE_DIR=" + filepath.Join(r.Dir, "terraform"),
		"SWIM_CACHE_TERRAFORM=" + filepath.Join(r.Dir, "terraform"),
		"GOMODCACHE=" + filepath.Join(r.Dir, "go"),
		"GOFLAGS=-trimpath -mod=readonly",
	} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "GOCACHE") || strings.Contains(got, "npm_config_cache") {
		t.Errorf("env sets an undeclared or unchecked cache:\n%s", got)
	}
	// A GOFLAGS that already picks a -mod is the operator's.
	env, _ = r.Env(decls[1:], []string{"GOFLAGS=-mod=vendor"})
	if !slices.Contains(env, "GOFLAGS=-mod=vendor") {
		t.Errorf("GOFLAGS -mod overridden: %v", env)
	}
}

func TestUseEvictAndPrune(t *testing.T) {
	r, err := Open(filepath.Join(t.TempDir(), "swim"), "repo-abc123")
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	old := now
	now = func() time.Time { return clock }
	t.Cleanup(func() { now = old })
	fill := func(name string, size int, day int) {
		dir, err := r.Path(name)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "blob"), make([]byte, size), 0o600)
		clock = time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC)
		r.Touch(name, day)
	}
	fill("terraform", 3000, 1)
	fill("npm", 2000, 2)
	fill("go", 1000, 3)

	// terraform is the least recently used, but a running lane holds it.
	held, err := r.Use("terraform")
	if err != nil {
		t.Fatal(err)
	}
	if !r.InUse("terraform") || r.InUse("npm") {
		t.Fatal("InUse wrong")
	}
	if err := r.Remove("terraform"); err == nil {
		t.Error("removed a cache in use")
	}
	gone, err := r.Evict(4500)
	if err != nil || fmt.Sprint(gone) != "[npm]" {
		t.Fatalf("Evict(4500) = %v %v, want [npm] (terraform is held)", gone, err)
	}
	held.Close()

	gone, _ = r.Evict(1500)
	if fmt.Sprint(gone) != "[terraform]" {
		t.Errorf("Evict(1500) = %v, want [terraform]", gone)
	}
	list, _ := r.List()
	if len(list) != 1 || list[0].Name != "go" || list[0].Size != 1000 || fmt.Sprint(list[0].Lanes) != "[3]" {
		t.Errorf("after eviction: %+v", list)
	}

	fill("npm", 10, 20)
	gone, _ = r.Prune(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), 0)
	if fmt.Sprint(gone) != "[go]" {
		t.Errorf("Prune(older than 10 Oct) = %v, want [go]", gone)
	}
}

func TestRemoveReadOnlyTree(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "swim"), "repo-abc123")
	dir, _ := r.Path("go")
	mod := filepath.Join(dir, "example.com", "m@v1.0.0")
	os.MkdirAll(mod, 0o755)
	os.WriteFile(filepath.Join(mod, "go.mod"), []byte("module m\n"), 0o444)
	os.Chmod(mod, 0o555) // as Go leaves its module cache
	if err := r.Remove("go"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("go cache still there: %v", err)
	}
}

func TestWriteLockWaits(t *testing.T) {
	r, _ := Open(filepath.Join(t.TempDir(), "swim"), "repo-abc123")
	unlock, err := r.Write("terraform", nil)
	if err != nil {
		t.Fatal(err)
	}
	waited := make(chan bool, 1)
	done := make(chan struct{})
	go func() {
		u, err := r.Write("terraform", func() { waited <- true })
		if err == nil {
			u()
		}
		close(done)
	}()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("second writer didn't wait")
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("second writer never got the lock")
	}
}

func TestOriginURL(t *testing.T) {
	cfg := "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = git@github.com:up/api.git\n[remote \"origin\"]\n\turl = git@github.com:org/api.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
	if got := originURL(cfg); got != "git@github.com:org/api.git" {
		t.Errorf("originURL = %q", got)
	}
	if got := originURL("[core]\n"); got != "" {
		t.Errorf("no origin: %q", got)
	}
	// A real repo, and a worktree of it, share the remote.
	root := t.TempDir()
	run := func(dir string, args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(root, "init", "-q")
	run(root, "remote", "add", "origin", "git@github.com:org/api.git")
	run(root, "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(t.TempDir(), "wt")
	run(root, "worktree", "add", "-q", wt)
	a, _ := Slug(root, "")
	b, _ := Slug(wt, "")
	if remoteFn(wt) != "git@github.com:org/api.git" || a[len(a)-6:] != b[len(b)-6:] {
		t.Errorf("worktree: remote %q, slugs %q %q", remoteFn(wt), a, b)
	}
}
