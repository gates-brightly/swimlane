package lane

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheHeaderAndPinning(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "infra"), 0o755)
	os.WriteFile(filepath.Join(root, "infra", ".terraform.lock.hcl"), []byte("provider \"aws\" {}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("# Round: plan\n# Cache: terraform @infra/.terraform.lock.hcl\n"), 0o755)
	info, err := ReadScript(root, 1)
	if err != nil || len(info.Problems) > 0 {
		t.Fatalf("%v %q", err, info.Problems)
	}
	if got := fmt.Sprint(info.Cache); got != "[{terraform [infra/.terraform.lock.hcl]}]" {
		t.Errorf("Cache = %s", got)
	}
	for _, kv := range info.Extra {
		if kv.K == "Cache" {
			t.Error("Cache: kept as an unknown header key")
		}
	}
	// The lockfile is part of what approvals cover.
	_, files, _ := ScriptParts(root, 1)
	if fmt.Sprint(files) != "[infra/.terraform.lock.hcl]" {
		t.Errorf("ScriptParts files = %v", files)
	}
	b1, _ := ScriptBytes(root, 1)
	os.WriteFile(filepath.Join(root, "infra", ".terraform.lock.hcl"), []byte("provider \"aws\" { version = \"6\" }\n"), 0o644)
	b2, _ := ScriptBytes(root, 1)
	if string(b1) == string(b2) {
		t.Error("changing the lockfile doesn't change the script hash")
	}

	// Problems are reported, not dropped.
	os.WriteFile(filepath.Join(root, "lane.2.sh"), []byte("# Round: x\n# Cache: terraform maven @pom.xml\n"), 0o755)
	info, _ = ReadScript(root, 2)
	if p := strings.Join(info.Problems, "; "); !strings.Contains(p, `unknown preset "maven"`) || !strings.Contains(p, "terraform needs its lockfile") {
		t.Errorf("problems = %q", info.Problems)
	}

	// The sidecar wins, as for every key.
	os.WriteFile(filepath.Join(root, "go.sum"), []byte("x\n"), 0o644)
	os.WriteFile(filepath.Join(root, "lane.1.yml"), []byte("cache: go @go.sum\n"), 0o644)
	info, _ = ReadScript(root, 1)
	if got := fmt.Sprint(info.Cache); got != "[{go [go.sum]}]" {
		t.Errorf("sidecar Cache = %s", got)
	}
	_, files, _ = ScriptParts(root, 1)
	if !strings.Contains(fmt.Sprint(files), "go.sum") {
		t.Errorf("sidecar lockfile not pinned: %v", files)
	}
}
