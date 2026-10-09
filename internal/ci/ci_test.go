package ci

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/logparse"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDetect(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		flag string
		want string
	}{
		{map[string]string{"GITHUB_ACTIONS": "true", "CI": "true"}, "", "github"},
		{map[string]string{"GITLAB_CI": "true", "CI": "true"}, "", "gitlab"},
		{map[string]string{"CI": "true"}, "", "generic"},
		{map[string]string{}, "", "generic"},
		{map[string]string{"GITHUB_ACTIONS": "true"}, "gitlab", "gitlab"},
	} {
		p, err := Detect(env(tc.env), tc.flag)
		if err != nil || p.Name() != tc.want {
			t.Errorf("%v %q: got %v %v, want %s", tc.env, tc.flag, p, err, tc.want)
		}
	}
	if _, err := Detect(env(nil), "jenkins"); err == nil {
		t.Error("unknown provider accepted")
	}
}

func TestFormats(t *testing.T) {
	var b bytes.Buffer
	gh := GitHub{}
	gh.Group(&b, "swim 1 PASS - a: b", true)
	gh.EndGroup(&b)
	gh.Annotate(&b, Error, "swim 2: cut, over", "FAIL  x (exit 1)\nmore 100%")
	want := "::group::swim 1 PASS - a: b\n::endgroup::\n::error title=swim 2%3A cut%2C over::FAIL  x (exit 1)%0Amore 100%25\n"
	if b.String() != want {
		t.Errorf("github:\n%q\nwant\n%q", b.String(), want)
	}
	b.Reset()
	gl := &GitLab{}
	gl.Group(&b, "swim 3 FAIL - Deploy API", true)
	gl.EndGroup(&b)
	s := b.String()
	if !strings.Contains(s, ":swim_1_swim_3_fail_deploy_api[collapsed=true]\r\x1b[0Kswim 3 FAIL - Deploy API\n") ||
		!strings.Contains(s, "section_end:") || !strings.Contains(s, ":swim_1_swim_3_fail_deploy_api\r") {
		t.Errorf("gitlab: %q", s)
	}
	b.Reset()
	Generic{}.Annotate(&b, Warning, "swim 1: x", "DRIFT  y")
	if b.String() != "swim: warning: swim 1: x: DRIFT  y\n" {
		t.Errorf("generic: %q", b.String())
	}
}

func TestFindBase(t *testing.T) {
	dir := t.TempDir()
	push := filepath.Join(dir, "push.json")
	os.WriteFile(push, []byte(`{"before":"abc123def4567890"}`), 0o644)
	newBranch := filepath.Join(dir, "new.json")
	os.WriteFile(newBranch, []byte(`{"before":"0000000000000000000000000000000000000000"}`), 0o644)
	for _, tc := range []struct {
		name     string
		p        Provider
		env      map[string]string
		explicit string
		ref      string
		merge    bool
		all      bool
		err      bool
	}{
		{"explicit", Generic{}, nil, "v1.2", "v1.2", false, false, false},
		{"explicit zero", Generic{}, nil, "0000000", "", false, true, false},
		{"github push", GitHub{}, map[string]string{"GITHUB_EVENT_NAME": "push", "GITHUB_EVENT_PATH": push}, "", "abc123def4567890", false, false, false},
		{"github new branch", GitHub{}, map[string]string{"GITHUB_EVENT_NAME": "push", "GITHUB_EVENT_PATH": newBranch}, "", "", false, true, false},
		{"github pr", GitHub{}, map[string]string{"GITHUB_EVENT_NAME": "pull_request", "GITHUB_BASE_REF": "main"}, "", "origin/main", true, false, false},
		{"gitlab mr", &GitLab{}, map[string]string{"CI_MERGE_REQUEST_DIFF_BASE_SHA": "f00d", "CI_COMMIT_BEFORE_SHA": "beef"}, "", "f00d", false, false, false},
		{"gitlab push", &GitLab{}, map[string]string{"CI_COMMIT_BEFORE_SHA": "beef"}, "", "beef", false, false, false},
		{"gitlab new branch", &GitLab{}, map[string]string{"CI_COMMIT_BEFORE_SHA": "0000000000"}, "", "", false, true, false},
		{"generic without base", Generic{}, nil, "", "", false, false, true},
	} {
		b, err := FindBase(env(tc.env), tc.p, tc.explicit)
		if (err != nil) != tc.err || b.Ref != tc.ref || b.Merge != tc.merge || b.All != tc.all {
			t.Errorf("%s: %+v %v", tc.name, b, err)
		}
	}
}

func reports() []LaneReport {
	exit1 := 1
	r := &logparse.Round{Title: "deploy", Run: "r-1", Finished: true, ExitCode: 1, Pass: 1, Fail: 1, Drift: 1, Skip: 1,
		Results: []logparse.Result{
			{Kind: "PASS", Label: "plan", Stage: "check", Step: true, Dur: 1.5},
			{Kind: "FAIL", Label: "apply", Detail: "exit 1", Stage: "change", Step: true, Dur: 2, Output: []string{"boom <&>"}},
			{Kind: "SKIP", Label: "delete table", Detail: "dry run: set X=1 to approve", Stage: "change"},
			{Kind: "DRIFT", Label: "queue policy differs", Stage: "verify"},
		}}
	return []LaneReport{
		{N: 1, Title: "deploy", State: "failed", Exit: &exit1, Duration: 3.5, Round: r, Lines: []string{"== ROUND x", "  FAIL  apply (exit 1)"}},
		{N: 2, Title: "after", State: "skipped", Reason: "swim 1 failed"},
		{N: 3, Title: "after too", State: "skipped", Reason: "swim 1 failed"},
	}
}

func TestAnnotateAndSections(t *testing.T) {
	var b bytes.Buffer
	Annotate(&b, Generic{}, reports())
	want := "swim: error: swim 1: deploy: FAIL  apply (exit 1)\n" +
		"swim: notice: swim 1: deploy: delete table: dry run: set X=1 to approve\n" +
		"swim: warning: swim 1: deploy: DRIFT  queue policy differs\n" +
		"swim: notice: swim: 2 lanes skipped: swim 2, 3 (swim 1 failed)\n"
	if b.String() != want {
		t.Errorf("annotations:\n%s\nwant\n%s", b.String(), want)
	}
	b.Reset()
	Sections(&b, GitHub{}, reports())
	if s := b.String(); !strings.HasPrefix(s, "==== swim 1 FAIL - deploy (0:04) ====\n== ROUND x\n") || strings.Contains(s, "::group::") {
		t.Errorf("a failed lane stays open (no group) on GitHub:\n%s", s)
	}
}

func TestJUnit(t *testing.T) {
	var b bytes.Buffer
	if err := JUnit(&b, reports()); err != nil {
		t.Fatal(err)
	}
	var got junitSuites
	if err := xml.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	if len(got.Suites) != 3 || got.Tests != 5 || got.Failures != 1 || got.Skipped != 3 {
		t.Fatalf("totals: %+v", got)
	}
	s1 := got.Suites[0]
	if s1.Cases[1].Failure == nil || s1.Cases[1].Failure.Body != "boom <&>" || s1.Cases[2].Skipped == nil {
		t.Errorf("suite 1: %+v", s1)
	}
	if c := got.Suites[1].Cases[0]; c.Skipped == nil || c.Skipped.Message != "swim 1 failed" {
		t.Errorf("skipped lane: %+v", c)
	}
}

func TestMarkdown(t *testing.T) {
	var b bytes.Buffer
	Markdown(&b, reports(), "r-1", "0123456789abcdef", 1)
	for _, want := range []string{"## swim ci: failed", "commit `0123456789ab`", "| swim 1 | `` | FAIL | 1 | 1 | 1 | 1 |", "FAIL  apply (exit 1)", "| swim 2 | `` | SKIP |"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("summary missing %q:\n%s", want, b.String())
		}
	}
}
