package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A lane runs in its staged copy: a file it reads that its manifest doesn't
// pin isn't there, so editing it after approval changes nothing it runs.
// # Workspace: live runs it in the working tree, and is part of what an
// approval signs.
func TestLaneRunsStaged(t *testing.T) {
	r := newRepo(t, "")
	r.write("helper.sh", "echo SAFE\n", 0o755)
	r.script(1, "use a helper", `run "helper" bash helper.sh`)
	unpin(t, r) // r.script's lint --fix pins it (workspace-file)
	out, _ := r.swim("run", "1", "--plain")
	if !strings.Contains(out, "FAIL") || strings.Contains(out, "SAFE") {
		t.Errorf("an unpinned helper ran:\n%s", out)
	}
	// The failure says why, on the terminal and in the log.
	hint := "helper.sh is in the repo but not in this lane's staged copy: add it to # Includes:"
	contains(t, "the hint", out, hint)
	contains(t, "the hint, logged", r.log(1), hint)

	// Pinned with # Includes:, it runs; editing it changes the manifest.
	r.script(1, "use a helper", "# Includes: helper.sh\n"+`run "helper" bash helper.sh`)
	before := manifestOf(t, r)
	contains(t, "pinned", r.mustSwim("run", "1", "--plain"), "PASS")
	r.write("helper.sh", "echo DESTRUCTIVE\n", 0o755)
	if manifestOf(t, r) == before {
		t.Error("editing a pinned helper left the manifest as it was")
	}

	// Live: it reads the working tree, and that's in the manifest.
	r.script(1, "use a helper", "# Workspace: live\n"+`run "helper" bash helper.sh`)
	live := manifestOf(t, r)
	contains(t, "live", r.mustSwim("run", "1", "--plain"), "PASS")
	r.script(1, "use a helper", `run "helper" bash helper.sh`)
	unpin(t, r)
	if manifestOf(t, r) == live {
		t.Error("# Workspace: live isn't in the manifest")
	}
}

// unpin drops lane 1's # Includes: lines.
func unpin(t *testing.T, r *repo) {
	t.Helper()
	p := filepath.Join(r.root, "lane.1.sh")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var keep []string
	for _, l := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(l, "# Includes:") {
			keep = append(keep, l)
		}
	}
	os.WriteFile(p, []byte(strings.Join(keep, "\n")), 0o755)
}

// manifestOf is lane 1's manifest hash as swim approve shows it.
func manifestOf(t *testing.T, r *repo) string {
	t.Helper()
	out, _ := r.swim("approve", "1", "--all", "--key", "/nonexistent")
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "approving manifest ") {
			return l
		}
	}
	t.Fatalf("no manifest in:\n%s", out)
	return ""
}
