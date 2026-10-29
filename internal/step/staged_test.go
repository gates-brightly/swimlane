package step

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A failed step's command names files the repo has and its staged copy
// doesn't; outside a staged copy, nothing is suggested.
func TestStagedMisses(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	for _, f := range []string{"deploy.yaml", "scripts/helper.sh", "lane.1.sh"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0o755)
		os.WriteFile(filepath.Join(root, f), nil, 0o644)
	}
	staged := filepath.Join(root, ".swim", "runs", "r-1", "1")
	os.MkdirAll(staged, 0o755)
	os.WriteFile(filepath.Join(staged, "lane.1.sh"), nil, 0o644)

	t.Chdir(staged)
	got := stagedMisses(root, []string{"bash", "-c", "cat deploy.yaml | grep x; bash scripts/helper.sh --config=deploy.yaml", "lane.1.sh", "-v", "$HOME/x", "/etc/passwd", "../../x"})
	if strings.Join(got, ",") != "deploy.yaml,scripts/helper.sh" {
		t.Errorf("staged: %v", got)
	}
	t.Chdir(root)
	if got := stagedMisses(root, []string{"cat", "nope.yaml"}); got != nil {
		t.Errorf("in the working tree: %v", got)
	}
}
