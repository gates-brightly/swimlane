package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cacheRepo is a repo whose caches live in a temp directory, never the
// developer's own.
func cacheRepo(t *testing.T) (*repo, string) {
	r := newRepo(t, "")
	dir := filepath.Join(t.TempDir(), "cache")
	r.mustSwim("config", "cache_dir", dir)
	return r, dir
}

func TestCachePresetPersistsBetweenRounds(t *testing.T) {
	r, dir := cacheRepo(t)
	r.write("package-lock.json", `{"lockfileVersion": 3}`+"\n", 0o644)
	r.script(1, "npm with a cache", `# Cache: npm @package-lock.json
run "see the cache" bash -c 'echo "cache=$npm_config_cache swim=$SWIM_CACHE_NPM gocache=${GOCACHE:-unset} tf=${TF_PLUGIN_CACHE_DIR:-unset}"; ls "$npm_config_cache"; touch "$npm_config_cache/tarball-$SWIM_RUN"'`)
	r.mustSwim("run", "1", "--plain")
	r.mustSwim("run", "1", "--plain")
	log := r.log(1)
	if !strings.Contains(log, "| cache="+dir) || !strings.Contains(log, "gocache=unset tf=unset") {
		t.Fatalf("the lane's environment:\n%s", log)
	}
	// The second round sees what the first left.
	rounds := strings.Split(log, "== ROUND")
	if !strings.Contains(rounds[len(rounds)-1], "| tarball-") {
		t.Errorf("the second round's cache is empty:\n%s", rounds[len(rounds)-1])
	}
	// Under /tmp, a cache directory gets the user's id, and only they can
	// use it.
	base := fmt.Sprintf("%s-%d", dir, os.Getuid())
	if fi, err := os.Stat(base); err != nil || fi.Mode().Perm() != 0o700 || !strings.Contains(log, "| cache="+base+"/") {
		t.Errorf("cache base %s: %v %v", base, fi, err)
	}
	out := r.mustSwim("cache", "ls")
	if !strings.Contains(out, "npm") || !strings.Contains(out, "by swim 1") {
		t.Errorf("swim cache ls:\n%s", out)
	}
	out = r.mustSwim("cache", "ls", "--yaml")
	if !strings.Contains(out, "schema: swim.cache/v1") || !strings.Contains(out, "name: npm") {
		t.Errorf("swim cache ls --yaml:\n%s", out)
	}
	// The same environment when the lane runs directly with bash.
	c := r.cmd("bash", "lane.1.sh")
	direct, err := c.CombinedOutput()
	if err != nil || !strings.Contains(r.log(1), "| cache="+dir) {
		t.Fatalf("bash lane.1.sh: %v\n%s", err, direct)
	}
	r.mustSwim("cache", "rm", "npm")
	if out := r.mustSwim("cache", "ls"); strings.Contains(out, "npm ") {
		t.Errorf("after rm:\n%s", out)
	}
}

func TestCacheLockfileIsPinnedAndRequired(t *testing.T) {
	r, _ := cacheRepo(t)
	r.script(1, "go with a cache", `# Cache: go @go.sum
run "ok" true`)
	// No lockfile on disk: the lane doesn't start.
	out, code := r.swim("run", "1", "--plain")
	if code == 0 || !strings.Contains(out, "lockfile go.sum is missing") {
		t.Fatalf("missing lockfile: exit %d\n%s", code, out)
	}
	r.write("go.sum", "example.com/m v1.0.0 h1:abc=\n", 0o644)
	r.mustSwim("run", "1", "--plain")
	first := r.status().Get(1).ScriptSHA
	r.write("go.sum", "example.com/m v1.1.0 h1:def=\n", 0o644)
	r.mustSwim("run", "1", "--plain")
	if second := r.status().Get(1).ScriptSHA; first == "" || first == second {
		t.Errorf("a lockfile change doesn't change what's approved: %q -> %q", first, second)
	}
	// No lockfile named at all is a lint error.
	r.script(2, "terraform, no lockfile", `# Cache: terraform
run "ok" true`)
	out, code = r.swim("lint", "2")
	if code == 0 || !strings.Contains(out, "terraform needs its lockfile") {
		t.Errorf("swim lint: exit %d\n%s", code, out)
	}
}

// Terraform's plugin cache isn't safe for two inits at once: lanes that
// share it take turns, and only for the steps that write it.
func TestCacheTerraformInitsTakeTurns(t *testing.T) {
	r, _ := cacheRepo(t)
	r.write(".terraform.lock.hcl", "# providers\n", 0o644)
	os.MkdirAll(filepath.Join(r.root, "tools"), 0o755)
	// A stand-in terraform whose init fails if another init is in the cache.
	r.write("tools/terraform", `#!/usr/bin/env bash
[ "$1" = init ] || exit 0
busy="$TF_PLUGIN_CACHE_DIR/.init-running"
if [ -e "$busy" ]; then echo "two inits at once"; exit 1; fi
touch "$busy"; sleep 1; rm -f "$busy"; echo "init ok"
`, 0o755)
	for _, n := range []int{1, 2} {
		r.script(n, "tf", `# Cache: terraform @.terraform.lock.hcl
run "tf init" ./tools/terraform init
run "tf plan" ./tools/terraform plan`)
	}
	out, code := r.swim("run", "1", "2", "--plain")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s\n%s", code, out, r.log(1), r.log(2))
	}
	both := r.log(1) + r.log(2)
	if strings.Contains(both, "two inits at once") || strings.Count(both, "| init ok") != 2 {
		t.Errorf("inits overlapped:\n%s", both)
	}
}
