package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/display"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/status"
)

func TestCellSummary(t *testing.T) {
	got := cellSummary(6, map[string]string{"a": status.Passed, "b": status.Passed, "c": status.Failed, "d": status.Skipped}, 1)
	if got != "2/6 pass · 1 failed · 1 skipped · 1 running · 1 waiting" {
		t.Errorf("summary %q", got)
	}
	if got := cellSummary(2, map[string]string{"a": status.Passed, "b": status.Passed}, 0); got != "2/2 pass" {
		t.Errorf("all passed: %q", got)
	}
}

func matrixRepo(t *testing.T) (string, lane.Info) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("# Round: x $SVC\n# Job: job-matrix-1\n# Matrix: SVC=a,b,c\n"), 0o755)
	info, err := lane.ReadScript(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	return root, info
}

func names(cs []lane.Cell) string { return strings.Join(CellNames(cs), ",") }

func TestSelectCells(t *testing.T) {
	root, info := matrixRepo(t)
	o := Options{Root: root, Cfg: &config.Config{Settings: config.Settings{Lanes: 4}}}
	if got := names(selectCells(o, 1, info)); got != "a,b,c" {
		t.Errorf("all cells: %s", got)
	}
	o.Cells = map[int][]string{1: {"b"}}
	if got := names(selectCells(o, 1, info)); got != "b" {
		t.Errorf("named: %s", got)
	}
	// --failed: cells that passed in this round (same job) are left out.
	status.Update(root, 1, 4, func(l *status.Lane) {
		for c, st := range map[string]string{"a": status.Passed, "b": status.Failed} {
			e := l.CellEntry(c)
			e.State, e.Job = st, "job-matrix-1/"+c
		}
		e := l.CellEntry("c")
		e.State, e.Job = status.Passed, "an-older-job/c" // another round's pass doesn't count
	})
	o.Cells, o.FailedCells = nil, true
	if got := names(selectCells(o, 1, info)); got != "b,c" {
		t.Errorf("--failed: %s", got)
	}
}

// A pre-answer is pinned to the script it was given for, covers only the
// lane's known guards, and never an evidence-bound one.
func TestPreAnswers(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "lane.2.sh"), []byte("# Round: g\n# Guards: ALLOW_A  do a\n"), 0o755)
	os.WriteFile(filepath.Join(root, "lane.3.sh"), []byte("# Round: none\n"), 0o755)
	devnull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	defer devnull.Close()
	disp := display.New(display.Options{Out: devnull, Plain: true}, []display.LaneView{{N: 2}, {N: 3}})
	o := Options{Root: root, Cfg: &config.Config{Settings: config.Settings{Lanes: 4}}, RunID: "r-1"}
	p := newPreAnswers(map[int]bool{2: true, 3: true})
	p.disp = disp
	if msg := p.set(o, 3, "approved"); msg != "swim 3 has no guards to approve" {
		t.Errorf("no guards: %q", msg)
	}
	if msg := p.set(o, 4, "approved"); !strings.Contains(msg, "isn't in this run") {
		t.Errorf("not in run: %q", msg)
	}
	if msg := p.set(o, 2, "approved"); !strings.Contains(msg, "pre-approved (1 guards)") {
		t.Errorf("set: %q", msg)
	}
	if _, ok := p.use(o, approval.Request{Lane: 2, Flag: "OTHER"}); ok {
		t.Error("a pre-answer covered a guard the lane doesn't declare")
	}
	if _, ok := p.use(o, approval.Request{Lane: 2, Flag: "ALLOW_A", EvidenceRequired: true}); ok {
		t.Error("a pre-answer counted for an evidence-bound guard")
	}
	resp, ok := p.use(o, approval.Request{Lane: 2, Flag: "ALLOW_A"})
	if !ok || !resp.Approved || !resp.Pre || resp.At == "" {
		t.Errorf("use: %+v %v", resp, ok)
	}
	if len(p.unused()) != 0 {
		t.Error("a used pre-answer listed as unused")
	}
	// Re-set after use is refused; a changed script drops a fresh one.
	if msg := p.set(o, 2, "declined"); !strings.Contains(msg, "already used") {
		t.Errorf("after use: %q", msg)
	}
	p2 := newPreAnswers(map[int]bool{2: true})
	p2.disp = disp
	p2.set(o, 2, "approved")
	os.WriteFile(filepath.Join(root, "lane.2.sh"), []byte("# Round: g changed\n# Guards: ALLOW_A  do a\n"), 0o755)
	if _, ok := p2.use(o, approval.Request{Lane: 2, Flag: "ALLOW_A"}); ok {
		t.Error("a pre-answer applied to a changed script")
	}
	if u := p2.unused(); len(u) != 1 || u[0] != "swim 2" {
		t.Errorf("unused: %v", u)
	}
}
