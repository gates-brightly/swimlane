package lint

import (
	"fmt"
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
		{name: "guard not listed", script: before(c, "stage verify\n", "if guard ALLOW_X \"x\"; then run \"x\" true; fi\n"), code: "guard-unlisted", level: finding.Warn},
		{name: "guard not used", script: before(c, "# Timeout:", "# Guards: ALLOW_Z  never used\n"), code: "guard-unused", level: finding.Warn, line: 7},
		{name: "destructive outside guard", script: before(c, "stage verify\n", "run \"clean\" rm -rf build\n"), code: "destructive", level: finding.Warn},
		{name: "destructive after else", script: replace(c, "fi\n", "else\n  run \"destroy\" terraform destroy\nfi\n"), code: "destructive", level: finding.Warn},
		{name: "blocked", script: before(c, "stage verify\n", "run \"ship\" git push origin main\n"), code: "blocked", level: finding.Error},
		{name: "blocked from config", script: before(c, "stage verify\n", "run \"deploy\" make deploy-prod\n"), cfg: func(c *config.Config) { c.BlockedCommands = []string{"deploy-prod"} }, code: "blocked", level: finding.Error},
		{name: "secret echoed (auto)", script: before(c, "stage verify\n", "echo \"token: $DEPLOY_TOKEN\"\n"), code: "secret-echo", level: finding.Warn},
		{name: "secret echoed (secret_env)", script: before(c, "stage verify\n", "run \"show\" printf '%s' ${MY_KEY}\n"), cfg: func(c *config.Config) { c.SecretEnv = []string{"MY_KEY"} }, code: "secret-echo", level: finding.Warn},
		{name: "bad Timeout", script: replace(c, "# Timeout: 30m", "# Timeout: soon"), code: "header", level: finding.Warn, line: 7},
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
