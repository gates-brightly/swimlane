package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A syntax 1 script (no syntax line) doesn't stop the repo: it's its own
// lane's problem (swim lint, and swim run refuses that lane). It's never
// rewritten. A syntax 1 log does stop it, saying how to move it aside.
func TestCheckSyntax1(t *testing.T) {
	root := t.TempDir()
	old := "#!/usr/bin/env bash\n# Round: old round\neval \"$(swim lib)\"\nlane_init 1\nrun \"x\" true\nsummary\n"
	os.WriteFile(filepath.Join(root, "lane.2.sh"), []byte("#!/usr/bin/env bash\necho hi\n"), 0o755)
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte(old), 0o755)
	if err := Check(root); err != nil {
		t.Errorf("a syntax 1 script stopped the repo: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "lane.1.sh")); string(data) != old {
		t.Error("Check rewrote it")
	}
	os.MkdirAll(filepath.Join(root, ".swim", "logs"), 0o755)
	os.WriteFile(filepath.Join(root, ".swim", "logs", "agent3.log"), []byte("=== ROUND START\n"), 0o644)
	if err := Check(root); err == nil || !strings.Contains(err.Error(), "agent3.log is swim syntax 1") || !strings.Contains(err.Error(), "swim archive 3") {
		t.Errorf("a syntax 1 log: %v", err)
	}
}

func TestCheckRefusesNewer(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("# swim: syntax 3\neval \"$(swim lib)\"\nlane_init 1\n"), 0o755)
	if err := Check(root); err != nil {
		t.Errorf("syntax 3 scripts are readable: %v", err)
	}
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("# swim: syntax 9\neval \"$(swim lib)\"\nlane_init 1\n"), 0o755)
	if err := Check(root); err == nil {
		t.Error("a syntax 9 script passed Check")
	}
}
