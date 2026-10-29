package testwait

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Tests wait for events, never for time: no Go test (or test helper)
// sleeps or ticks, no lane script a test writes sleeps, and no e2e
// scenario script does. A wait goes through this package (Until, Watch,
// Exited) or a FIFO; a bound a test checks on purpose is a deadline, not a
// pause.
func TestNoSleepInTests(t *testing.T) {
	root := filepath.Join("..", "..")
	bashSleep := regexp.MustCompile(`(^|[\s;&|(` + "`" + `'"])sleep\s+["$0-9.]`)
	timed := map[string]bool{"Sleep": true, "Tick": true, "NewTicker": true}
	helper := func(p string) bool { // non-test files that are test rigs
		return strings.Contains(p, filepath.Join("internal", "testwait")) || strings.Contains(p, filepath.Join("e2e", "cmd"))
	}
	count := 0
	for _, dir := range []string{"internal", "cmd", "e2e"} {
		filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				if d != nil && d.IsDir() && d.Name() == "fixtures" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			switch {
			case strings.HasSuffix(p, ".go") && (strings.HasSuffix(p, "_test.go") || helper(p)):
				count++
				f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
				if err != nil {
					t.Errorf("%s: %v", rel, err)
					return nil
				}
				ast.Inspect(f, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok {
						if x, ok := sel.X.(*ast.Ident); ok && x.Name == "time" && timed[sel.Sel.Name] {
							t.Errorf("%s: time.%s: wait for an event (testwait), not a time", rel, sel.Sel.Name)
						}
					}
					if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && bashSleep.MatchString(lit.Value) {
						t.Errorf("%s: a script that sleeps: %.60s", rel, lit.Value)
					}
					return true
				})
			case strings.HasPrefix(rel, "e2e"+string(filepath.Separator)) && isShell(p):
				count++
				data, _ := os.ReadFile(p)
				for i, line := range strings.Split(string(data), "\n") {
					if code, _, _ := strings.Cut(line, "#"); bashSleep.MatchString(code) {
						t.Errorf("%s:%d: sleeps: wait for an event (e2etool await, wait_until), not a time", rel, i+1)
					}
				}
			}
			return nil
		})
	}
	if count < 50 {
		t.Fatalf("scanned only %d files; the scan is broken", count)
	}
}

// isShell is a shell script: .sh, .tmpl, or a file starting with #! ... sh.
func isShell(p string) bool {
	if strings.HasSuffix(p, ".sh") || strings.HasSuffix(p, ".tmpl") {
		return true
	}
	data, err := os.ReadFile(p)
	if err != nil || !strings.HasPrefix(string(data), "#!") {
		return false
	}
	first, _, _ := strings.Cut(string(data), "\n")
	return strings.Contains(first, "sh")
}
