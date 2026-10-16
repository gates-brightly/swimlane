package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanSources(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"lib/a.sh", "lib/b.sh", "helpers.py", "mod/__init__.py", "util.ts"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte("x\n"), 0o644)
	}
	src := `# source lib/commented.sh
source lib/a.sh
if true; then . "lib/b.sh"; fi
source "$LIB/c.sh"
. ~/.bashrc
source /etc/profile
source ` + filepath.Join(root, "lib/a.sh") + `
source lib/missing.sh
`
	var got []string
	for _, s := range scanSources(root, "lane.1.sh", src) {
		if s.Dynamic {
			got = append(got, "dynamic:"+s.Arg)
		} else {
			got = append(got, s.Path)
		}
	}
	if strings.Join(got, " ") != `lib/a.sh lib/b.sh dynamic:"$LIB/c.sh" lib/a.sh` {
		t.Errorf("bash: %v", got)
	}
	got = nil
	for _, s := range scanSources(root, "lane.1.py", "import swim\nimport helpers\nfrom mod import x\nimport os\n") {
		got = append(got, s.Path)
	}
	if strings.Join(got, " ") != "helpers.py mod/__init__.py" {
		t.Errorf("python: %v", got)
	}
	if s := scanSources(root, "lane.1.ts", `import { u } from "./util";`); len(s) != 1 || s[0].Path != "util.ts" {
		t.Errorf("ts: %v", s)
	}
}
