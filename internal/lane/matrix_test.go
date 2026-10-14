package lane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cellNames(cs []Cell) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, " ")
}

func TestMatrixExpansion(t *testing.T) {
	info := ParseScript("# Round: cut over $SVC in ${ENV}\n# Matrix: ENV=staging,prod\n# Matrix: SVC=orders,search\n# Matrix-Exclude: ENV=prod,SVC=search\n# Matrix-Parallel: 2\n# Matrix-Fail: stop\n# Locks: tf/$ENV/$SVC, shared\n")
	if got := cellNames(info.Cells()); got != "ENV=staging,SVC=orders ENV=staging,SVC=search ENV=prod,SVC=orders" {
		t.Fatalf("cells: %s", got)
	}
	if info.MatrixParallel != 2 || info.MatrixFail != "stop" || len(info.Problems) != 0 {
		t.Errorf("info: %+v", info)
	}
	c, ok := info.Cell("ENV=prod,SVC=orders")
	if !ok {
		t.Fatal("cell by name")
	}
	ci := info.ForCell(c)
	if ci.Round != "cut over orders in prod" || strings.Join(ci.Locks, ",") != "tf/prod/orders,shared" {
		t.Errorf("for cell: %q %v", ci.Round, ci.Locks)
	}
	if _, ok := info.Cell("search"); !ok {
		t.Error("a unique part of a cross-product name should find its cell")
	}
	if _, ok := info.Cell("orders"); ok {
		t.Error("an ambiguous part found a cell")
	}
	perCell, perLane := CellLocks(info.Locks)
	if len(perCell) != 1 || len(perLane) != 1 {
		t.Errorf("locks %v %v", perCell, perLane)
	}
	env := strings.Join(c.Env("/r", 3), " ")
	if !strings.Contains(env, "SWIM_CELL=ENV=prod,SVC=orders") || !strings.Contains(env, "SVC=orders") || !strings.Contains(env, "agent3.env-prod.svc-orders.log") {
		t.Errorf("env %s", env)
	}
	if r, cell, ok := ParseCellRef("3[orders]"); !ok || r != "3" || cell != "orders" {
		t.Errorf("cell ref %q %q %v", r, cell, ok)
	}
	if _, _, ok := ParseCellRef("3"); ok {
		t.Error("plain ref parsed as a cell")
	}
	bad := ParseScript("# Round: x\n# Matrix: 9X=a\n# Matrix-Parallel: none\n")
	if len(bad.Problems) != 2 {
		t.Errorf("problems %q", bad.Problems)
	}
}

func TestMatrixFileValuesAndScriptBytes(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "services.txt"), []byte("# services\norders\n\nbilling\n"), 0o644)
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte("# Round: x $SVC\n# Matrix: SVC=@services.txt\n"), 0o755)
	info, err := ReadScript(root, 1)
	if err != nil || cellNames(info.Cells()) != "orders billing" {
		t.Fatalf("%v %s %q", err, cellNames(info.Cells()), info.Problems)
	}
	b1, _ := ScriptBytes(root, 1)
	os.WriteFile(filepath.Join(root, "services.txt"), []byte("orders\n"), 0o644)
	b2, _ := ScriptBytes(root, 1)
	if string(b1) == string(b2) || !strings.Contains(string(b1), "# swim: included file services.txt\n") {
		t.Errorf("script bytes don't cover the value file:\n%s", b1)
	}
	t.Setenv(CellEnv, "orders")
	info, err = ReadScript(root, 1)
	if err != nil || info.Round != "x orders" || filepath.Base(Log(root, 1)) != "agent1.orders.log" || filepath.Base(PIDFile(root, 1)) != "lane1.orders.pid" {
		t.Errorf("in a cell: %v %q %s %s", err, info.Round, Log(root, 1), PIDFile(root, 1))
	}
	t.Setenv(CellEnv, "nope")
	if _, err := ReadScript(root, 1); err == nil {
		t.Error("unknown cell: no error")
	}
}
