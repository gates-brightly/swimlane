package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/lookup"
)

func TestParse(t *testing.T) {
	f, err := Parse("u", []byte("# comment\nA=1\nB = 'two words'\nC=\"$literal\"\nD=x # trailing\n\n[repo ~/r]\nE=e\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range f.General {
		got[e.Key] = e.Value
	}
	if got["A"] != "1" || got["B"] != "two words" || got["C"] != "$literal" || got["D"] != "x" || len(f.Repos) != 1 || f.Repos[0].Entries[0].Value != "e" {
		t.Errorf("parsed %v %+v", got, f.Repos)
	}
	for src, want := range map[string]string{
		"export A=1\n":         "u:1: no `export`",
		"A=$HOME\n":            "u:1: A: no expansion",
		"A=$(id)\n":            "no expansion",
		"A='x\n":               "unterminated quote",
		"1A=x\n":               "expected KEY=VALUE",
		"just words\n":         "expected KEY=VALUE",
		"A=1\n[lane 2]\nB=2\n": "u:2: [lane N] sections go in the repo's .env.swim",
		"[thing]\n":            "sections are [repo PATH]",
	} {
		if _, err := Parse("u", []byte(src), true); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", src, err, want)
		}
	}
	if _, err := Parse(".env.swim", []byte("A=1\n"), false); err == nil || !strings.Contains(err.Error(), "before any [lane N]") {
		t.Errorf("repo file, top-level key: %v", err)
	}
	if _, err := Parse(".env.swim", []byte("[repo /x]\n"), false); err == nil {
		t.Error("repo file, [repo] section: no error")
	}
	rf, err := Parse(".env.swim", []byte("[lane 2]\nA=1\n[lane 3]\nA=3\n[lane 2]\nB=2\n"), false)
	if err != nil || len(rf.LaneEntries(2)) != 2 || rf.LaneEntries(3)[0].Value != "3" {
		t.Errorf("lanes: %+v %v", rf, err)
	}
}

func TestResolvePrecedenceAndGuards(t *testing.T) {
	xdg, root := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	os.MkdirAll(filepath.Join(xdg, "swim"), 0o755)
	os.WriteFile(filepath.Join(xdg, "swim", "env.swim"), []byte("K=general\nG=general\n[repo "+root+"]\nK=repo\nR=repo\n[repo /elsewhere]\nK=other\n"), 0o644)
	os.WriteFile(filepath.Join(root, RepoFile), []byte("[lane 1]\nK=lane\n[lane 2]\nK=lane2\n"), 0o644)
	cfg := &config.Config{Settings: config.Settings{Env: map[string]string{"K": "yml", "Y": "yml", "R": "yml"}}}
	r, err := Resolve(root, cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range r.Vars {
		got[v.Key] = v.Value + "@" + v.Scope
	}
	want := map[string]string{"K": "lane@lane 1", "G": "general@general", "R": "repo@repo", "Y": "yml@swim.yml"}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	if len(r.Sources) != 4 || r.Sources[3] != ".env.swim[lane 1]" {
		t.Errorf("sources %q", r.Sources)
	}
	h1 := r.SHA256()
	os.WriteFile(filepath.Join(root, RepoFile), []byte("[lane 1]\nK=changed\n"), 0o644)
	r2, _ := Resolve(root, cfg, 1)
	if h1 == "" || r2.SHA256() == h1 {
		t.Error("env hash didn't change with a value")
	}
	if (&Resolved{}).SHA256() != "" {
		t.Error("empty env has a hash")
	}

	info := lane.Info{Guards: []lane.Guard{{Flag: "K"}}}
	if c := GuardConflicts(r2, info, nil); len(c) != 1 || c[0].Key != "K" || !strings.Contains(c[0].Error(), ".env.swim:2 sets K") {
		t.Errorf("script guard: %+v", c)
	}
	lk := &lookup.Lookup{Require: []lookup.Rule{{Guards: []string{"G*"}}}}
	if c := GuardConflicts(r2, lane.Info{}, lk); len(c) != 1 || c[0].Key != "G" || !strings.Contains(c[0].Error(), `matches guards "G*"`) {
		t.Errorf("lookup glob: %+v", c)
	}
	if _, err := Check(root, cfg, 1, info, nil); err == nil {
		t.Error("Check allowed a guard flag")
	}
}

func TestMigrateRC(t *testing.T) {
	entries, problems := MigrateRC([]byte("# rc\nexport STAGE=dev\nTABLE='a b'\n"))
	if len(problems) != 0 || len(entries) != 2 || entries[1].Value != "a b" {
		t.Errorf("%+v %q", entries, problems)
	}
	if _, problems := MigrateRC([]byte("source ~/.nvm/nvm.sh\nX=$(date)\n")); len(problems) != 2 {
		t.Errorf("problems %q", problems)
	}
	root := t.TempDir()
	if err := AppendLane(root, 3, []Entry{{Key: "A", Value: "it's \"q\""}, {Key: "B", Value: "x y"}}, "moved"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, RepoFile))
	f, err := Parse(RepoFile, data, false)
	if err != nil || len(f.LaneEntries(3)) != 2 || f.LaneEntries(3)[0].Value != `it's "q"` || f.LaneEntries(3)[1].Value != "x y" {
		t.Errorf("round trip: %v\n%s", err, data)
	}
}

func TestDuplicates(t *testing.T) {
	xdg, root := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	os.MkdirAll(filepath.Join(xdg, "swim"), 0o755)
	os.WriteFile(filepath.Join(xdg, "swim", "env.swim"), []byte("A=1\nB=2\nA=3\n[repo /x]\nA=4\n"), 0o644)
	os.WriteFile(filepath.Join(root, RepoFile), []byte("[lane 1]\nT=1\n[lane 2]\nT=2\n[lane 1]\nT=3\n"), 0o644)
	got := strings.Join(Duplicates(root), "\n")
	if !strings.Contains(got, "env.swim:3 sets A again in [general] (first at line 1") || !strings.Contains(got, ".env.swim:6 sets T again in [lane 1] (first at line 2") {
		t.Errorf("duplicates:\n%s", got)
	}
	if strings.Count(got, "\n") != 1 {
		t.Errorf("want 2 duplicates (A in a repo section and T in lane 2 aren't):\n%s", got)
	}
}
