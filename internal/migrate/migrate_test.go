package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A syntax 1 script is refused, not rewritten; a plain program has no
// swim syntax and passes.
func TestCheckRefusesSyntax1(t *testing.T) {
	root := t.TempDir()
	old := "#!/usr/bin/env bash\n# Round: old round\neval \"$(swim lib)\"\nlane_init 1\nrun \"x\" true\nsummary\n"
	os.WriteFile(filepath.Join(root, "lane.2.sh"), []byte("#!/usr/bin/env bash\necho hi\n"), 0o755)
	if err := Check(root); err != nil {
		t.Errorf("a plain program: %v", err)
	}
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte(old), 0o755)
	if err := Check(root); err == nil || !strings.Contains(err.Error(), "lane.1.sh is swim syntax 1") {
		t.Errorf("a syntax 1 script: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "lane.1.sh")); string(data) != old {
		t.Error("Check rewrote it")
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
