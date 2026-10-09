package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// setEnv replaces one variable in the repo's environment (every copy:
// newRepo appends PATH after the inherited one).
func setEnv(r *repo, key, value string) {
	var env []string
	for _, kv := range r.env {
		if !strings.HasPrefix(kv, key+"=") {
			env = append(env, kv)
		}
	}
	r.env = append(env, key+"="+value)
}

// lastEnv is the value a child process sees: the last copy wins.
func lastEnv(r *repo, key string) string {
	v := ""
	for _, kv := range r.env {
		if k, val, ok := strings.Cut(kv, "="); ok && k == key {
			v = val
		}
	}
	return v
}

func writeLane(t *testing.T, r *repo, n int, s string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.root, "lane."+strconv.Itoa(n)+".sh"), []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
}

// hermeticPath drops PATH entries holding a swim other than the one under
// test, so a developer's installed swim doesn't change doctor's findings.
func hermeticPath(r *repo) {
	var keep []string
	for _, d := range filepath.SplitList(lastEnv(r, "PATH")) {
		if _, err := os.Stat(filepath.Join(d, "swim")); err == nil && d != filepath.Dir(bin) {
			continue
		}
		keep = append(keep, d)
	}
	setEnv(r, "PATH", strings.Join(keep, string(os.PathListSeparator)))
}

func TestLintExitCodes(t *testing.T) {
	r := newRepo(t, "")
	out := r.mustSwim("new", "1", "a round")
	contains(t, "swim new", out, "swim lint 1")
	// The fresh template still has its placeholder step.
	if out, code := r.swim("lint", "1"); code != 1 || !strings.Contains(out, "[placeholder]") {
		t.Fatalf("template lint: %d\n%s", code, out)
	}

	r.script(1, "clean", "stage check\nrun \"ok\" true")
	out = r.mustSwim("lint")
	contains(t, "clean lint", out, "no problems found")

	// A warning: exit 0, but 1 with --strict.
	r.script(2, "bash 4", "stage check\ndeclare -A seen\nrun \"ok\" true")
	out, code := r.swim("lint")
	if code != 0 || !strings.Contains(out, "lane.2.sh:7") || !strings.Contains(out, "[bash4]") || !strings.Contains(out, "0 errors, 1 warning") {
		t.Fatalf("warning: %d\n%s", code, out)
	}
	if out, code := r.swim("lint", "--strict"); code != 1 {
		t.Fatalf("--strict: %d\n%s", code, out)
	}

	// An error: exit 1. Naming lanes lints only those.
	r.script(3, "errexit", "set -e\nstage check\nrun \"ok\" true")
	if out, code := r.swim("lint"); code != 1 || !strings.Contains(out, "lane.3.sh:6") || !strings.Contains(out, "[set-e]") || !strings.Contains(out, "1 error, 1 warning") {
		t.Fatalf("error: %d\n%s", code, out)
	}
	if out, code := r.swim("lint", "1"); code != 0 || strings.Contains(out, "lane.3.sh") {
		t.Fatalf("lint 1: %d\n%s", code, out)
	}
	out, code = r.swim("lint", "--yaml")
	if code != 1 {
		t.Fatalf("--yaml exit %d", code)
	}
	contains(t, "--yaml", out, "schema: swim.lint/v1", "errors: 1", "warnings: 1", "code: set-e", "file: lane.3.sh", "line: 6", "level: error")

	// Read-only: a syntax 1 script is reported, not migrated.
	v1 := "#!/usr/bin/env bash\n# Round: old style\n_swim_lib=$(\"${SWIM_BIN:-swim}\" lib) || exit 1; eval \"$_swim_lib\"\nlane_init 4\nrun \"x\" true\nsummary\n"
	writeLane(t, r, 4, v1)
	out, _ = r.swim("lint", "4")
	contains(t, "syntax 1", out, "[syntax]")
	if data, _ := os.ReadFile(filepath.Join(r.root, "lane.4.sh")); string(data) != v1 {
		t.Errorf("lint changed lane.4.sh:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "migrations")); err == nil {
		t.Error("lint migrated files")
	}
}

func TestRunLintPreflight(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "fine", "run \"ok\" true")
	r.script(2, "errexit", "set -o errexit\nrun \"never\" true")
	writeLane(t, r, 3, "#!/usr/bin/env bash\n# swim: syntax 2\n# Round: wrong lane\n_swim_lib=$(\"${SWIM_BIN:-swim}\" lib) || exit 1; eval \"$_swim_lib\"\nlane_init 4\nrun \"never\" true\nsummary\n")

	out, code := r.swim("run")
	if code != 2 {
		t.Fatalf("run exit %d, want 2:\n%s", code, out)
	}
	contains(t, "pre-flight", out, "lane.2.sh:6", "[set-e]", "lane.3.sh:5", "[lane-init]", "refusing to start")
	if r.log(1) != "" || r.log(2) != "" {
		t.Errorf("a lane ran despite the pre-flight:\n%s", out)
	}

	// Only the lanes being run are checked; warnings never refuse.
	r.script(4, "warns only", "declare -A seen\nrun \"ok\" true")
	r.mustSwim("run", "1", "4")

	// A disable comment with a reason lets the lane run.
	r.script(2, "errexit on purpose", "set -o errexit  # swim:lint-ignore set-e this round must die on the first failure\nrun \"ran\" true")
	if out, code := r.swim("run", "2"); code != 0 || !strings.Contains(r.log(2), "PASS  ran") {
		t.Fatalf("ignored set-e: %d\n%s", code, out)
	}
	// Without a reason it disables nothing.
	r.script(2, "errexit, no reason", "set -o errexit  # swim:lint-ignore set-e\nrun \"ran\" true")
	if out, code := r.swim("run", "2"); code != 2 || !strings.Contains(out, "[set-e]") {
		t.Fatalf("reasonless ignore: %d\n%s", code, out)
	}
}

// foreignSwim cross-compiles a tiny program for a platform this host can't
// run and returns the directory holding it, named swim.
func foreignSwim(t *testing.T) (dir, platform string) {
	t.Helper()
	goos, goarch := "darwin", "arm64"
	if runtime.GOOS == "darwin" {
		goos, goarch = "linux", "amd64"
	}
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644)
	os.WriteFile(filepath.Join(src, "go.mod"), []byte("module tiny\ngo 1.21\n"), 0o644)
	dir = t.TempDir()
	cmd := exec.Command("go", "build", "-buildvcs=false", "-ldflags=-s -w", "-o", filepath.Join(dir, "swim"), ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0", "GOFLAGS=-buildvcs=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-compile: %v\n%s", err, out)
	}
	return dir, goos + "/" + goarch
}

func TestDoctor(t *testing.T) {
	r := newRepo(t, "")
	hermeticPath(r)
	out := r.mustSwim("doctor")
	contains(t, "healthy repo", out, "0 errors, 0 warnings")

	// Break things: a stale .gitignore block, a stale pid file, a lane script
	// tracked by git, and a swim for another platform first on PATH.
	gi := filepath.Join(r.root, ".gitignore")
	os.WriteFile(gi, []byte("node_modules/\n# >>> swim >>>\nnext[0-9]*.sh\n.swim/\n# <<< swim <<<\ndist/\n"), 0o644)
	os.WriteFile(filepath.Join(r.root, ".swim", "lane2.pid"), []byte("2147483646\n"), 0o644)
	r.script(1, "tracked", "run \"ok\" true")
	if out, err := r.cmd("git", "add", "-f", "lane.1.sh").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	dir, plat := foreignSwim(t)
	setEnv(r, "PATH", dir+string(os.PathListSeparator)+lastEnv(r, "PATH"))
	cfgPath := filepath.Join(envOf(r, "XDG_CONFIG_HOME"), "swim", "config.yml")
	cfgBefore, _ := os.ReadFile(cfgPath)
	laneBefore, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh"))

	out, code := r.swim("doctor")
	if code != 1 {
		t.Fatalf("doctor exit %d, want 1:\n%s", code, out)
	}
	contains(t, "doctor", out,
		filepath.Join(dir, "swim"), "error", "built for "+plat, "exec format error", "[binary-platform]",
		"[path-multiple]", "[swim-bin]",
		".gitignore", "missing lane.[0-9]*.sh", "has old next[0-9]*.sh", "[gitignore]",
		".swim/lane2.pid", "[stale-pid]",
		"git tracks lane.1.sh", "[tracked]",
		"swim doctor --fix")

	out, code = r.swim("doctor", "--yaml")
	if code != 1 {
		t.Fatalf("doctor --yaml exit %d", code)
	}
	contains(t, "doctor --yaml", out, "schema: swim.doctor/v1", "code: binary-platform", "code: stale-pid")

	// --fix repairs only the safe items.
	out, code = r.swim("doctor", "--fix")
	if code != 1 {
		t.Fatalf("doctor --fix exit %d (the wrong binary is still there):\n%s", code, out)
	}
	contains(t, "--fix", out, "fixed: rewrote swim's block in .gitignore", "fixed: removed .swim/lane2.pid", "[binary-platform]", "[tracked]")
	for _, gone := range []string{"[gitignore]", "[stale-pid]"} {
		if strings.Contains(out, gone) {
			t.Errorf("--fix left %s:\n%s", gone, out)
		}
	}
	data, _ := os.ReadFile(gi)
	contains(t, ".gitignore", string(data), "node_modules/\n# >>> swim >>>\nlane.[0-9]*.sh\n", "# <<< swim <<<\ndist/\n")
	if _, err := os.Stat(filepath.Join(r.root, ".swim", "lane2.pid")); !os.IsNotExist(err) {
		t.Error("stale pid file still there")
	}
	if _, err := os.Stat(filepath.Join(dir, "swim")); err != nil {
		t.Error("--fix touched the binary on PATH")
	}
	if after, _ := os.ReadFile(cfgPath); !bytes.Equal(after, cfgBefore) {
		t.Error("--fix changed the config")
	}
	if after, _ := os.ReadFile(filepath.Join(r.root, "lane.1.sh")); !bytes.Equal(after, laneBefore) {
		t.Error("--fix changed a lane script")
	}
	if st, _ := r.cmd("git", "ls-files", "lane.1.sh").Output(); strings.TrimSpace(string(st)) != "lane.1.sh" {
		t.Error("--fix changed the git index")
	}
}

func TestDoctorBrokenConfig(t *testing.T) {
	r := newRepo(t, "")
	hermeticPath(r)
	cfgPath := filepath.Join(envOf(r, "XDG_CONFIG_HOME"), "swim", "config.yml")
	os.WriteFile(cfgPath, []byte("defaults: [not, a, mapping\n"), 0o644)
	out, code := r.swim("doctor")
	if code != 1 || !strings.Contains(out, "[config-parse]") {
		t.Fatalf("broken config: %d\n%s", code, out)
	}
	os.WriteFile(cfgPath, []byte("defaults:\n  lanes: 2\n  header_env: [STAGE, DEPLOY_TOKEN]\n  toolchain: exit 7\n"), 0o644)
	out, code = r.swim("doctor")
	if code != 1 {
		t.Fatalf("doctor exit %d:\n%s", code, out)
	}
	contains(t, "doctor", out, "[config-repo]", "header_env lists DEPLOY_TOKEN", "[header-env-secret]", "toolchain \"exit 7\" fails", "[toolchain]")
}
