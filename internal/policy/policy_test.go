package policy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	p := Patterns([]string{"gh  pr merge", "git push"}) // duplicate and extra spaces
	if len(p) != 4 || p[3] != "gh pr merge" {
		t.Fatalf("Patterns = %q", p)
	}
	cases := map[string][]string{
		"git   push origin main":                            {"git push"},
		"bash -c git add . && git commit -m x && git  push": {"git push", "git commit"},
		"git status":          nil,
		"GIT PUSH":            nil, // case-sensitive
		"echo never git push": {"git push"},
		"gh pr merge 3":       {"gh pr merge"},
	}
	for cmd, want := range cases {
		got := Match(cmd, p)
		if strings.Join(got, ",") != strings.Join(sortLike(want, p), ",") {
			t.Errorf("Match(%q) = %q, want %q", cmd, got, want)
		}
	}
	if !IsBuiltin("git  push") || IsBuiltin("gh pr merge") {
		t.Error("IsBuiltin")
	}
}

// sortLike orders want the way Match reports (pattern order).
func sortLike(want, patterns []string) []string {
	var out []string
	for _, p := range patterns {
		for _, w := range want {
			if w == p {
				out = append(out, p)
			}
		}
	}
	return out
}

func TestScanScript(t *testing.T) {
	src := "#!/usr/bin/env bash\n# Round: never git push here (header)\n# git commit in a comment\nrun \"x\" true\nrun \"publish\" git push   # and a comment with git pull\n  echo ok\nrun \"y\" bash -c 'git commit -m x'\n"
	hits := ScanScript(src, Builtins)
	if len(hits) != 2 || hits[0].Line != 5 || hits[0].Pattern != "git push" || hits[1].Line != 7 || hits[1].Pattern != "git commit" {
		t.Fatalf("hits = %+v", hits)
	}
}

// swim's own code may run git only to read. Every exec.Command("git", ...)
// outside tests must pass a literal, read-only subcommand first. Adding a
// write is a deliberate change to this list.
func TestSwimOnlyReadsGit(t *testing.T) {
	allowed := map[string]bool{"rev-parse": true, "diff": true, "log": true, "ls-files": true, "status": true}
	root := filepath.Join("..", "..")
	count := 0
	for _, dir := range []string{"internal", "cmd"} {
		filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Errorf("%s: %v", path, err)
				return nil
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
					return true
				}
				args := call.Args
				if sel.Sel.Name == "CommandContext" && len(args) > 0 {
					args = args[1:]
				}
				if len(args) == 0 {
					return true
				}
				if lit, ok := args[0].(*ast.BasicLit); !ok || lit.Value != `"git"` {
					return true
				}
				count++
				if len(args) < 2 {
					t.Errorf("%s: git called without a subcommand", path)
					return true
				}
				lit, ok := args[1].(*ast.BasicLit)
				if !ok {
					t.Errorf("%s: git subcommand must be a literal so this test can check it", path)
					return true
				}
				sub, _ := strconv.Unquote(lit.Value)
				if !allowed[sub] {
					t.Errorf("%s: swim runs `git %s`; swim only reads git (allowed: rev-parse, diff, log, ls-files, status)", path, sub)
				}
				return true
			})
			return nil
		})
	}
	if count == 0 {
		t.Fatal("found no git calls; the scan is broken")
	}
}
