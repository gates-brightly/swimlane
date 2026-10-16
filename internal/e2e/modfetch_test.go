package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// modSource is a git repository holding module pager under pager/, tagged
// pager/vX.Y.Z for each version given; git rewrites
// https://example.com/org/mods to it (url.<base>.insteadOf), so the
// fetch goes through swim's real path with no network.
func modSource(t *testing.T, r *repo, versions ...string) (dir string, tag func(ver, says string)) {
	t.Helper()
	dir = t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=m", "GIT_AUTHOR_EMAIL=m@example.com", "GIT_COMMITTER_NAME=m", "GIT_COMMITTER_EMAIL=m@example.com")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	tag = func(ver, says string) {
		os.MkdirAll(filepath.Join(dir, "pager"), 0o755)
		os.WriteFile(filepath.Join(dir, "pager", "module.yml"), []byte("schema: swim.module/v1\nname: pager\nversion: "+ver+"\nkind: bash\ncommands: [pager]\neffects:\n  network: [pager.example.com]\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "pager", "pager.sh"), []byte("pager() { echo \""+says+": $*\"; }\n"), 0o644)
		git("add", "-A")
		git("commit", "-q", "-m", ver)
		git("tag", "pager/v"+ver)
	}
	for _, v := range versions {
		tag(v, "pager "+v)
	}
	r.env = append(r.env, "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=url.file://"+dir+".insteadOf", "GIT_CONFIG_VALUE_0=https://example.com/org/mods")
	return dir, tag
}

// A fetched module: named by source address and version, refused until
// swim modules get fetches it and pins it in .swim.lock; then it runs from
// .swim/modules/ as pinned, a changed copy is refused, a new tag isn't
// taken until --upgrade, and swim.yml must allow its address.
func TestFetchedModules(t *testing.T) {
	r := newRepo(t, "")
	_, tag := modSource(t, r, "2.0.0", "2.1.0", "3.0.0")
	r.write("swim.yml", "modules:\n  allow: [example.com/org/mods//*]\n", 0o644)
	r.modScript(1, "page someone", `# Modules: example.com/org/mods//pager@2
mod pager "the deploy is done"`)

	contains(t, "plan before get", r.mustSwim("plan", "1"), "blocked: module example.com/org/mods//pager@2: example.com/org/mods//pager isn't fetched: swim modules get")
	contains(t, "get", r.mustSwim("modules", "get"), ".swim.lock: example.com/org/mods//pager 2.1.0 (new, commit ")
	lock, _ := os.ReadFile(filepath.Join(r.root, ".swim.lock"))
	contains(t, "lock", string(lock), "fetched:", "example.com/org/mods//pager:", "version: 2.1.0", "commit: ")
	contains(t, "get again", r.mustSwim("modules", "get"), "pager 2.1.0: pinned, and here")

	r.swim("run", "1", "--plain")
	contains(t, "run", r.log(1), "PASS  pager the deploy is done", "pager 2.1.0: the deploy is done")
	contains(t, "plan", r.mustSwim("plan", "1"), "pager 2.1.0 (example.com/org/mods//pager")

	// A changed copy in the store is refused, and get fetches it again.
	stores, _ := filepath.Glob(filepath.Join(r.root, ".swim", "modules", "*", "pager.sh"))
	if len(stores) != 1 {
		t.Fatalf("store: %v", stores)
	}
	os.Chmod(stores[0], 0o644)
	os.WriteFile(stores[0], []byte("pager() { echo tampered; }\n"), 0o644)
	contains(t, "plan tampered", r.mustSwim("plan", "1"), "isn't what .swim.lock pins")
	r.mustSwim("modules", "get")
	contains(t, "plan restored", r.mustSwim("plan", "1"), "pager 2.1.0")

	// A newer tag within the major: kept at 2.1.0 until --upgrade.
	tag("2.2.0", "pager 2.2.0")
	contains(t, "get keeps", r.mustSwim("modules", "get"), "pager 2.1.0: pinned, and here")
	contains(t, "upgrade", r.mustSwim("modules", "get", "--upgrade", "pager"), ".swim.lock: example.com/org/mods//pager 2.1.0 -> 2.2.0")
	r.swim("run", "1", "--plain")
	contains(t, "run upgraded", r.log(1), "pager 2.2.0: the deploy is done")

	// doctor: the pinned copy is here; one that's gone is an error.
	if out := must(r.swim("doctor")); strings.Contains(out, "module-fetched") {
		t.Errorf("doctor with the module here:\n%s", out)
	}
	stores, _ = filepath.Glob(filepath.Join(r.root, ".swim", "modules", "*", "pager.sh"))
	for _, s := range stores {
		os.Chmod(filepath.Dir(s), 0o755)
		os.Chmod(s, 0o644)
		os.RemoveAll(filepath.Dir(s))
	}
	contains(t, "doctor missing", must(r.swim("doctor")), "[module-fetched]", "pager 2.2.0 isn't in .swim/modules/")
	r.mustSwim("modules", "get")

	// swim.yml must allow the address.
	r.write("swim.yml", "modules:\n  allow: [notify]\n", 0o644)
	out, code := r.swim("modules", "get")
	if code == 0 || !strings.Contains(out, "isn't allowed: add it (or a glob matching it) to swim.yml modules: allow:") {
		t.Errorf("get unallowed: %d %s", code, out)
	}
	contains(t, "plan unallowed", r.mustSwim("plan", "1"), "isn't allowed")
}
