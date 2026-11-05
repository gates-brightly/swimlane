package lane

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestAssociated(t *testing.T) {
	root := t.TempDir()
	write := func(name, src string) {
		t.Helper()
		os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755)
		if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := Associated(root); len(got) != 0 {
		t.Errorf("empty repo: %v", got)
	}
	write("lane.2.sh", "# Round: two\n")
	write("lane.3.sh", "# swim:stub\n# done\n") // a stub: not in use
	write("lane.4.notes", "not a script")       // not executable, so not a script
	os.Chmod(filepath.Join(root, "lane.4.notes"), 0o644)
	write(".swim/logs/lane5.log", "")              // a current log
	write(".swim/logs/lane6.prev-cutover.log", "") // archived: no
	write(".swim/logs/lane7.check.log", "")        // a check log: no
	if got := Associated(root); fmt.Sprint(got) != "[2 5]" {
		t.Errorf("associated: %v, want [2 5]", got)
	}
	// Archiving the log drops lane 5.
	os.Rename(filepath.Join(root, ".swim/logs/lane5.log"), filepath.Join(root, ".swim/logs/lane5.prev-x.log"))
	if got := Associated(root); fmt.Sprint(got) != "[2]" {
		t.Errorf("after archive: %v", got)
	}
}

func TestArchiveMovesJobLogs(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(LogDir(root), 0o755)
	for _, f := range []string{"lane1.log", "lane1.2.log", "lane1.5.9.log", "lane10.log", "lane1.check.log"} {
		os.WriteFile(filepath.Join(LogDir(root), f), nil, 0o644)
	}
	if _, err := Archive(root, 1, "done"); err != nil {
		t.Fatal(err)
	}
	var names []string
	entries, _ := os.ReadDir(LogDir(root))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if got := fmt.Sprint(names); got != "[lane1.check.log lane1.prev-done.2.log lane1.prev-done.5.9.log lane1.prev-done.log lane10.log]" {
		t.Errorf("after archive: %s", got)
	}
	if got := Associated(root); fmt.Sprint(got) != "[10]" {
		t.Errorf("in use after archive: %v", got)
	}
}
