package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/syntax"
)

func TestMigrateSyntax1Script(t *testing.T) {
	root := t.TempDir()
	old := "#!/usr/bin/env bash\n# Round: old round\neval \"$(swim lib)\"\nlane_init 1\nrun \"x\" true\nsummary\n"
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte(old), 0o755)
	// A plain program (no lane_init) has no swim syntax: never rewritten.
	plain := "#!/usr/bin/env bash\necho hi\n"
	os.WriteFile(filepath.Join(root, "lane.2.sh"), []byte(plain), 0o755)
	res, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0] != "lane.1.sh" || res.Backup == "" {
		t.Fatalf("result: %+v", res)
	}
	data, _ := os.ReadFile(filepath.Join(root, "lane.1.sh"))
	if syntax.OfScript(string(data)) != syntax.Current || !strings.Contains(string(data), "run \"x\" true") {
		t.Errorf("migrated:\n%s", data)
	}
	if b, _ := os.ReadFile(filepath.Join(root, res.Backup, "lane.1.sh")); string(b) != old {
		t.Error("no backup of the original")
	}
	if p, _ := os.ReadFile(filepath.Join(root, "lane.2.sh")); string(p) != plain {
		t.Error("a plain program was rewritten")
	}
	again, _ := Run(root)
	if len(again.Files) != 0 {
		t.Errorf("a second run migrated again: %+v", again)
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
