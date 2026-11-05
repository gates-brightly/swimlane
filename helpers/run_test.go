package helpers

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePath puts scripts named by bin first on PATH, for this test.
func fakePath(t *testing.T, bin map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range bin {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func run(fn string, args ...string) (int, string) {
	var b bytes.Buffer
	code := Run(fn, args, &b)
	return code, b.String()
}

func TestRunExitCodes(t *testing.T) {
	fakePath(t, map[string]string{
		"node": "echo v20.11.1\n",
		"go":   "[ \"$1\" = version ] && { echo go version go1.27.1 darwin/arm64; exit 0; }; echo 'flag provided but not defined: -version' >&2; exit 2\n",
		"aws":  `echo '{"Account": "111111111111", "Arn": "arn:aws:iam::111111111111:user/op"}'` + "\n",
	})
	for _, c := range []struct {
		fn   string
		args []string
		code int
		want string
	}{
		{"tool_gate", []string{"node", "20"}, 0, "v20.11.1"},
		{"tool_gate", []string{"node", "20.11"}, 0, ""},
		{"tool_gate", []string{"node", "2"}, 1, "want node 2, got 20.11.1"},
		{"tool_gate", []string{"go", "1.27"}, 0, "go version go1.27.1"},
		{"tool_gate", []string{"no-such-tool"}, 1, "no-such-tool: not on PATH"},
		{"tool_gate", nil, 2, "tool_gate needs a command name"},
		{"aws_gate", nil, 0, "arn:aws:iam::111111111111:user/op"},
		{"aws_gate", []string{"111111111111"}, 0, "account 111111111111"},
		{"aws_gate", []string{"222222222222"}, 1, "want account 222222222222"},
		{"aws_gate", []string{"1234"}, 2, "12-digit account id"},
		{"gh_pr_state_gate", []string{"7", "OPEN"}, 2, "no repo"},
		{"gh_pr_head_gate", []string{"--repo", "o/r", "main", "abc"}, 2, "needs a pull request number, got 'main'"},
		{"nope", nil, 2, `no check "nope"`},
	} {
		code, out := run(c.fn, c.args...)
		if code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%s %v: exit %d, want %d with %q:\n%s", c.fn, c.args, code, c.code, c.want, out)
		}
	}
}

// Every check is run by a module function of the same name.
func TestEveryCheckHasItsFunction(t *testing.T) {
	defined := map[string]bool{}
	for _, m := range Modules() {
		for _, f := range Funcs(m) {
			for _, n := range f.Names {
				defined[n] = true
			}
		}
	}
	for _, c := range Checks() {
		if !defined[c] {
			t.Errorf("check %s: no module documents a function %s", c, c)
		}
	}
}
