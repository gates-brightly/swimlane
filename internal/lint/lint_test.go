package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/finding"
)

// clean is a lane script every check passes.
func clean(n int) string {
	return fmt.Sprintf(`#!/usr/bin/env bash
# swim: syntax 2
# Round: clean round %[1]d
# Job:   clean-job-%[1]d
# After:
# Guards: ALLOW_DROP  drop the old table (2026-10-09, migrated)
# Timeout: 30m
#
# Notes in prose: set -e is banned; mapfile too. # Round: not a key
_swim_lib=$("${SWIM_BIN:-swim}" lib) || { echo "swim not found" >&2; exit 1; }
eval "$_swim_lib"
lane_init %[1]d
stage snapshot
snapshot "tables" aws dynamodb list-tables
stage check
gate "precheck" test -f go.mod
run "quoted bash 4 is a sub-shell" bash -c 'mapfile -t x < /dev/null; echo "${x,,}"'
run "echo is not destructive" echo "would delete nothing"
stage change
if guard ALLOW_DROP "drop old table (2026-10-09)"; then
  run "drop table" aws dynamodb delete-table --table-name old
fi
stage verify
run "token length only" echo "${#API_TOKEN}"
cat <<EOF
declare -A in a heredoc is text
EOF
summary
`, n)
}

func cfg4() *config.Config {
	return &config.Config{Settings: config.Settings{Lanes: 4}}
}

func codes(fs []finding.Finding) string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Code)
	}
	return strings.Join(out, " ")
}

func TestCleanScriptHasNoFindings(t *testing.T) {
	for n := 1; n <= 4; n++ {
		scripts := map[int]string{1: clean(1), 2: clean(2), 3: clean(3), 4: clean(4)}
		if fs := Lint(cfg4(), scripts, n); len(fs) != 0 {
			t.Errorf("lane %d: %+v", n, fs)
		}
	}
}

func replace(s, old, new string) string {
	if !strings.Contains(s, old) {
		panic("fixture: no " + old)
	}
	return strings.Replace(s, old, new, 1)
}

func before(s, anchor, add string) string { return replace(s, anchor, add+anchor) }

func TestEachCheck(t *testing.T) {
	c := clean(1)
	cases := []struct {
		name    string
		script  string
		other   string // lane 2's script, default clean(2)
		cfg     func(*config.Config)
		code    string
		level   string
		line    int
		message string
	}{
		{name: "no Round", script: replace(c, "# Round: clean round 1\n", ""), code: "round", level: finding.Error},
		{name: "placeholder", script: before(c, "stage check\n", `snapshot "current state" echo "replace with a read-only describe/list command"`+"\n"), code: "placeholder", level: finding.Error, line: 15},
		{name: "template braces", script: replace(c, "# Round: clean round 1", "# Round: {{.Goal}}"), code: "placeholder", level: finding.Error, line: 3},
		{name: "After unknown lane", script: replace(c, "# After:", "# After: 9"), code: "after", level: finding.Error, line: 5, message: "no swim 9"},
		{name: "After itself", script: replace(c, "# After:", "# After: 1"), code: "after", level: finding.Error, line: 5, message: "itself"},
		{name: "After unknown job", script: replace(c, "# After:", "# After: deadbeef-gone"), code: "after", level: finding.Error, message: "no lane holds job"},
		{name: "cycle", script: replace(c, "# After:", "# After: 2"), other: replace(clean(2), "# After:", "# After: clean-job-1"), code: "cycle", level: finding.Error, line: 5, message: "swim 1, swim 2"},
		{name: "set -e", script: before(c, "_swim_lib=", "set -euo pipefail\n"), code: "set-e", level: finding.Error, line: 10},
		{name: "set -o errexit", script: before(c, "_swim_lib=", "set -o errexit\n"), code: "set-e", level: finding.Error},
		{name: "shebang -e", script: replace(c, "#!/usr/bin/env bash", "#!/bin/bash -e"), code: "set-e", level: finding.Error, line: 1},
		{name: "no lane_init", script: replace(c, "lane_init 1\n", ""), code: "lane-init", level: finding.Error},
		{name: "wrong lane_init", script: replace(c, "lane_init 1", "lane_init 3"), code: "lane-init", level: finding.Error, line: 12, message: "lane_init 3"},
		{name: "no summary", script: replace(c, "summary\n", ""), code: "summary", level: finding.Warn},
		{name: "summary not last", script: c + "run \"late\" true\n", code: "summary", level: finding.Warn},
		{name: "unknown stage", script: replace(c, "stage verify", "stage deploy"), code: "stage", level: finding.Error, message: `"deploy"`},
		{name: "stage repeated", script: before(c, "summary\n", "stage check\n"), code: "stage-order", level: finding.Warn},
		{name: "stage out of order", script: replace(replace(c, "stage snapshot\n", ""), "stage verify", "stage verify\nstage snapshot"), code: "stage-order", level: finding.Warn, message: "snapshot after verify"},
		{name: "bash 4", script: before(c, "stage snapshot\n", "declare -A seen\n"), code: "bash4", level: finding.Warn, line: 13},
		{name: "bash 4 in double quotes", script: before(c, "stage snapshot\n", "echo \"${NAME^^}\"\n"), code: "bash4", level: finding.Warn},
		{name: "guard not listed", script: before(c, "stage verify\n", "if guard ALLOW_X \"x\"; then run \"x\" true; fi\n"), code: "guard-unlisted", level: finding.Error, message: "swim refuses it when it asks"},
		{name: "guard named at run time", script: before(c, "stage verify\n", "if guard \"$FLAG\" \"x\"; then run \"x\" true; fi\n"), code: "guard-dynamic", level: finding.Error, message: "# Guards: *"},
		{name: "guard not used", script: before(c, "# Timeout:", "# Guards: ALLOW_Z  never used\n"), code: "guard-unused", level: finding.Warn, line: 7},
		{name: "Cache unknown preset", script: before(c, "# Timeout:", "# Cache: maven @pom.xml\n"), code: "cache", level: finding.Error, line: 7, message: `unknown preset "maven"`},
		{name: "Cache no lockfile", script: before(c, "# Timeout:", "# Cache: terraform\n"), code: "cache", level: finding.Error, line: 7, message: "terraform needs its lockfile"},
		{name: "npm install with a cache", script: before(before(c, "# Timeout:", "# Cache: npm @package-lock.json\n"), "stage verify\n", "run \"deps\" npm install\n"), code: "cache-bypass", level: finding.Warn, message: "npm install"},
		{name: "pip without hashes", script: before(before(c, "# Timeout:", "# Cache: pip @requirements.txt\n"), "stage verify\n", "run \"deps\" pip install requests\n"), code: "cache-bypass", level: finding.Warn, message: "--require-hashes"},
		{name: "destructive outside guard", script: before(c, "stage verify\n", "run \"clean\" rm -rf build\n"), code: "destructive", level: finding.Warn},
		{name: "destructive after else", script: replace(c, "fi\n", "else\n  run \"destroy\" terraform destroy\nfi\n"), code: "destructive", level: finding.Warn},
		{name: "blocked", script: before(c, "stage verify\n", "run \"ship\" git push origin main\n"), code: "blocked", level: finding.Error},
		{name: "blocked from config", script: before(c, "stage verify\n", "run \"deploy\" make deploy-prod\n"), cfg: func(c *config.Config) { c.BlockedCommands = []string{"deploy-prod"} }, code: "blocked", level: finding.Error},
		{name: "secret echoed (auto)", script: before(c, "stage verify\n", "echo \"token: $DEPLOY_TOKEN\"\n"), code: "secret-echo", level: finding.Warn},
		{name: "secret echoed (secret_env)", script: before(c, "stage verify\n", "run \"show\" printf '%s' ${MY_KEY}\n"), cfg: func(c *config.Config) { c.SecretEnv = []string{"MY_KEY"} }, code: "secret-echo", level: finding.Warn},
		{name: "bad Timeout", script: replace(c, "# Timeout: 30m", "# Timeout: soon"), code: "header", level: finding.Warn, line: 7},
		{name: "removed header", script: before(c, "# Timeout:", "# Finally: 2\n"), code: "removed", level: finding.Error, line: 7, message: "# Finally:"},
		{name: "Modules header", script: before(c, "# Timeout:", "# Modules: notify\n"), code: "removed", level: finding.Error, line: 7, message: "# Modules: is gone"},
		{name: "evidence step", script: before(c, "stage verify\n", "evidence \"plan\" true\n"), code: "removed", level: finding.Error, message: "evidence steps are gone"},
		{name: "mod call", script: before(c, "stage verify\n", "mod notify - \"done\"\n"), code: "removed", level: finding.Error, message: "mod calls a module"},
		{name: "outcome handler", script: before(c, "stage snapshot\n", "on_failure() {\n  run \"undo\" true\n}\n"), code: "removed", level: finding.Error, line: 13, message: "on_failure"},
		{name: "go vet on an empty stage", script: before(c, "stage verify\n", "run \"vet\" go vet ./...\n"), code: "workspace-empty", level: finding.Warn, line: 23, message: "checks nothing"},
		{name: "gofmt in bash -c on an empty stage", script: before(c, "stage verify\n", "run \"fmt\" bash -c 'out=$(gofmt -s -l .); [ -z \"$out\" ]'\n"), code: "workspace-empty", level: finding.Warn, message: "gofmt"},
		{name: "cross-lane read", script: before(c, "stage verify\n", "run \"read\" cat shared/report.json\n"), other: before(clean(2), "stage verify\n", "run \"fetch\" curl -fsS -o shared/report.json https://example.com\n"), code: "cross-lane", level: finding.Info, message: "lane.2.sh:23 writes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cf := cfg4()
			if tc.cfg != nil {
				tc.cfg(cf)
			}
			other := tc.other
			if other == "" {
				other = clean(2)
			}
			fs := Lint(cf, map[int]string{1: tc.script, 2: other}, 1)
			if len(fs) != 1 || fs[0].Code != tc.code {
				t.Fatalf("want exactly one %s finding, got [%s]: %+v", tc.code, codes(fs), fs)
			}
			got := fs[0]
			if got.Level != tc.level || got.File != "lane.1.sh" || got.Message == "" {
				t.Errorf("finding: %+v", got)
			}
			if tc.line != 0 && got.Line != tc.line {
				t.Errorf("line %d, want %d: %+v", got.Line, tc.line, got)
			}
			if !strings.Contains(got.Message, tc.message) {
				t.Errorf("message %q lacks %q", got.Message, tc.message)
			}
		})
	}
}

func TestWorkspaceEmptyQuiet(t *testing.T) {
	vet := "run \"vet\" go vet ./...\n"
	for name, script := range map[string]string{
		"live":       before(before(clean(1), "stage verify\n", vet), "# Timeout:", "# Workspace: live\n# swim:lint-ignore-file workspace-live go vet reads the checkout\n"),
		"includes":   before(before(clean(1), "stage verify\n", vet), "# Timeout:", "# Includes: go.mod *.go\n"),
		"go version": before(clean(1), "stage verify\n", "run \"go\" go version\n"),
		"go env":     before(clean(1), "stage verify\n", "run \"go\" go env GOPATH\n"),
	} {
		if fs := Lint(cfg4(), map[int]string{1: script}, 1); len(fs) != 0 {
			t.Errorf("%s: %+v", name, fs)
		}
	}
}

func TestCrossLaneQuietWhenOrdered(t *testing.T) {
	reader := replace(before(clean(1), "stage verify\n", "run \"read\" cat shared/report.json\n"), "# After:", "# After: 2")
	writer := before(clean(2), "stage verify\n", "run \"fetch\" curl -fsS -o shared/report.json https://example.com\n")
	if fs := Lint(cfg4(), map[int]string{1: reader, 2: writer}, 1); len(fs) != 0 {
		t.Errorf("ordered lanes: %+v", fs)
	}
	// Config deps order lanes too.
	reader = before(clean(1), "stage verify\n", "run \"read\" cat shared/report.json\n")
	cf := cfg4()
	cf.Deps = map[int][]int{1: {2}}
	if fs := Lint(cf, map[int]string{1: reader, 2: writer}, 1); len(fs) != 0 {
		t.Errorf("config deps: %+v", fs)
	}
}

func TestDisableComments(t *testing.T) {
	bad := "run \"clean\" rm -rf build"
	c := clean(1)
	cases := []struct {
		name, script, want string
	}{
		{"same line", before(c, "stage verify\n", bad+"   # swim:lint-ignore destructive build output this round made\n"), ""},
		{"next line", before(c, "stage verify\n", "# swim:lint-ignore destructive build output this round made\n\n"+bad+"\n"), ""},
		{"two codes", before(c, "stage verify\n", "# swim:lint-ignore bash4,destructive both fine here\n"+bad+"; declare -A x\n"), ""},
		{"whole file", before(before(c, "stage verify\n", bad+"\n"), "#\n# Notes", "# swim:lint-ignore-file destructive the build dir is scratch\n"), ""},
		{"only the next line", before(c, "stage verify\n", "# swim:lint-ignore destructive just this one\n"+bad+"\n"+bad+"\n"), "destructive"},
		{"other code", before(c, "stage verify\n", "# swim:lint-ignore bash4 wrong code\n"+bad+"\n"), "destructive"},
		{"no reason", before(c, "stage verify\n", "# swim:lint-ignore destructive\n"+bad+"\n"), "destructive lint-ignore"},
		{"no code", before(c, "stage verify\n", "# swim:lint-ignore\n"+bad+"\n"), "destructive lint-ignore"},
		{"unknown code", before(c, "stage verify\n", "# swim:lint-ignore no-such-check because\n"), "lint-ignore"},
		{"errors too", before(c, "_swim_lib=", "set -e  # swim:lint-ignore set-e this round wants to die on the first error\n"), ""},
		{"file-level for a lineless finding", replace(before(c, "#\n# Notes", "# swim:lint-ignore-file summary the exit trap records the state\n"), "summary\n", ""), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := Lint(cfg4(), map[int]string{1: tc.script}, 1)
			var got []string
			for _, f := range fs {
				got = append(got, f.Code)
			}
			// Order-independent compare.
			want := strings.Fields(tc.want)
			if len(got) != len(want) {
				t.Fatalf("got [%s], want [%s]: %+v", codes(fs), tc.want, fs)
			}
			for _, w := range want {
				if !strings.Contains(" "+codes(fs)+" ", " "+w+" ") {
					t.Fatalf("got [%s], want [%s]", codes(fs), tc.want)
				}
			}
			for _, f := range fs {
				if f.Code == codeIgnore && strings.Contains(tc.want, "destructive") && f.Level != finding.Error {
					t.Errorf("a disable comment without a reason should be an error: %+v", f)
				}
			}
		})
	}
}

func TestScanViews(t *testing.T) {
	src := "echo 'a # not comment' \"b ${x,,} c\" # comment\ncat <<-'EOT'\n\tmapfile\n\tEOT\nrun \"multi\nline\" true\n"
	v := scan(src)
	if strings.Contains(v.code[0], "comment\"") || strings.Contains(v.code[0], "# comment") {
		t.Errorf("code kept the comment: %q", v.code[0])
	}
	if !strings.Contains(v.code[0], "a # not comment") {
		t.Errorf("code lost a quoted #: %q", v.code[0])
	}
	if strings.Contains(v.bare[0], "not") || !strings.Contains(v.bare[0], "${x,,}") {
		t.Errorf("bare: %q", v.bare[0])
	}
	if !v.doc[2] || !v.doc[3] || v.doc[1] || v.doc[4] {
		t.Errorf("heredoc lines: %v", v.doc)
	}
	if strings.TrimSpace(v.bare[2]) != "" {
		t.Errorf("heredoc body in bare: %q", v.bare[2])
	}
	if strings.Contains(v.bare[5], "line") || !strings.Contains(v.bare[5], "true") {
		t.Errorf("multi-line string: %q", v.bare[5])
	}
	for i := range v.raw {
		if len(v.raw[i]) != len(v.code[i]) || len(v.raw[i]) != len(v.bare[i]) {
			t.Errorf("line %d lengths differ", i)
		}
	}
}

func TestKnownCodes(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Checks {
		if seen[c.Code] {
			t.Errorf("duplicate code %s", c.Code)
		}
		seen[c.Code] = true
		if c.Preflight && c.Level != finding.Error {
			t.Errorf("%s: pre-flight checks must be errors", c.Code)
		}
	}
	if !KnownCode("set-e") || KnownCode("nope") {
		t.Error("KnownCode")
	}
}

// The secret store is gone: swim secret (bash, in a $(...) too) and an
// SDK's secret.get are each an error; the words in quoted text aren't.
func TestSecretStoreRemoved(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := clean(1)
	for _, tc := range []struct{ name, line string }{
		{"cli", "T=$(swim secret get gone)\n"},
		{"cli in quotes", "T=\"$(swim secret get gone)\"\n"},
		{"cli argument", "run \"query\" psql \"$(swim secret get PROD_DB)\" -c 'select 1'\n"},
		{"python sdk", "t = swim.secret.get(\"gone\")\n"},
		{"go sdk", "t, err := swim.Secret.Get(\"gone\")\n"},
	} {
		s := NewScript(1, before(c, "stage verify\n", tc.line))
		if strings.HasSuffix(tc.name, "sdk") {
			// An SDK lane: read as written (lane.1.py, lane.1.go).
			s = NewScript(1, "# Round: sdk\n"+tc.line)
			s.Name = "lane.1.py"
		}
		fs := lintScripts(cfg4(), map[int]*Script{1: s, 2: NewScript(2, clean(2))}, 1)
		if len(fs) != 1 || fs[0].Code != "removed" || fs[0].Level != finding.Error || !strings.Contains(fs[0].Message, "swim secret is gone") {
			t.Errorf("%s: %+v", tc.name, fs)
		}
	}
	if fs := Lint(cfg4(), map[int]string{1: before(c, "stage verify\n", "echo 'swim secret get x'\n"), 2: clean(2)}, 1); len(fs) != 0 {
		t.Errorf("quoted text: %+v", fs)
	}
}

// Modules are gone: the header, mod and mod_env (bash) and an SDK's
// module call are each an error; go mod download, and mod in quoted text,
// aren't module calls.
func TestModulesRemoved(t *testing.T) {
	c := replace(before(clean(1), "# Timeout:", "# Modules: notify tf\n"), "# swim: syntax 2", "# swim: syntax 4")
	c = before(c, "stage change\n", "run \"deps\" go mod download\nmod_env aws-session --profile p\necho \"mod notify x\"\n")
	c = before(c, "summary\n", "mod --timeout 30s notify \"#c\" \"done\" && echo sent\n")
	var lines []string
	for _, f := range Lint(cfg4(), map[int]string{1: c, 2: clean(2)}, 1) {
		if f.Code != "removed" || f.Level != finding.Error {
			t.Errorf("finding: %+v", f)
		}
		lines = append(lines, fmt.Sprint(f.Line))
	}
	if len(lines) != 3 || lines[0] != "7" {
		t.Errorf("removed findings on lines %v, want the header, mod_env and mod", lines)
	}
	py := "#!/usr/bin/env python3\n# swim: syntax 4\n# Round: r\nimport swim\nr = swim.lane()\nr.module(\"notify\", \"-\", \"hi\")\n"
	if fs := Lint(cfg4(), map[int]string{1: py, 2: clean(2)}, 1); !strings.Contains(codes(fs), "removed") {
		t.Errorf("an SDK module call: %+v", fs)
	}
}

// --fix adds the header lines the body needs, after the header block,
// with a guard's own action as its description; then the script lints
// clean of them. It never touches what's there.
func TestFix(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "lib"), 0o755)
	os.WriteFile(filepath.Join(root, "lib", "x.sh"), []byte("x=1\n"), 0o644)
	src := `#!/usr/bin/env bash
# swim: syntax 2
# Round: fix me
# Guards: ALLOW_A  a
#
# Goal: prose
_swim_lib=$("${SWIM_BIN:-swim}" lib) || exit 1; eval "$_swim_lib"
lane_init 1
source lib/x.sh
if guard ALLOW_A "a"; then run a true; fi
if guard ALLOW_B "drop the old table"; then run b true; fi
guard "$F" "dynamic" && run c true
summary
`
	os.WriteFile(filepath.Join(root, "lane.1.sh"), []byte(src), 0o755)
	cfg := &config.Config{Root: root, Settings: config.Settings{Lanes: 1}}
	fixed, err := Fix(root, cfg, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(fixed) != 1 || strings.Join(fixed[0].Added, "|") != "# Guards: ALLOW_B  drop the old table|# Guards: *  flags named at run time (say which, and why)|# Includes: lib/x.sh" {
		t.Fatalf("fixed: %+v", fixed)
	}
	got, _ := os.ReadFile(filepath.Join(root, "lane.1.sh"))
	if !strings.Contains(string(got), "# Guards: ALLOW_A  a\n# Guards: ALLOW_B  drop the old table\n# Guards: *") || !strings.Contains(string(got), "# Includes: lib/x.sh\n#\n# Goal: prose") {
		t.Errorf("script:\n%s", got)
	}
	if st, _ := os.Stat(filepath.Join(root, "lane.1.sh")); st.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", st.Mode())
	}
	fs, _ := Run(root, cfg, []int{1})
	for _, f := range fs {
		if f.Code == "guard-unlisted" || f.Code == "guard-dynamic" || f.Code == "source" {
			t.Errorf("after --fix: %+v", f)
		}
	}
	// Nothing left to add: nothing changes.
	if fixed, _ := Fix(root, cfg, []int{1}); len(fixed) != 0 {
		t.Errorf("second fix: %+v", fixed)
	}
}
