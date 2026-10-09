package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p := filepath.Join(dir, "swim", "config.yml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPathHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	if got := Path(); got != "/x/cfg/swim/config.yml" {
		t.Fatalf("Path() = %q", got)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if c.Lanes != DefaultLanes || c.HasRepo {
		t.Fatalf("got lanes=%d hasRepo=%v", c.Lanes, c.HasRepo)
	}
}

func TestLoadMergesRepoOverDefaults(t *testing.T) {
	writeConfig(t, `
defaults:
  lanes: 4
  header_env: [STAGE]
  runtime: node --version
repos:
  /repo/a:
    lanes: 6
    deps:
      2: [1]
      4: [2]
  /repo/b:
    header_env: [AWS_PROFILE]
`)
	a, err := Load("/repo/a")
	if err != nil {
		t.Fatal(err)
	}
	if a.Lanes != 6 || a.Runtime != "node --version" || strings.Join(a.HeaderEnv, ",") != "STAGE" {
		t.Fatalf("a = %+v", a.Settings)
	}
	if got := a.DepsOf(4); len(got) != 1 || got[0] != 2 {
		t.Fatalf("DepsOf(4) = %v", got)
	}
	b, err := Load("/repo/b")
	if err != nil {
		t.Fatal(err)
	}
	if b.Lanes != 4 || strings.Join(b.HeaderEnv, ",") != "AWS_PROFILE" || len(b.Deps) != 0 {
		t.Fatalf("b = %+v", b.Settings)
	}
}

func TestValidateRejectsCyclesAndRange(t *testing.T) {
	cases := map[string]string{
		"cycle":  "repos:\n  /r:\n    deps: {1: [2], 2: [3], 3: [1]}\n",
		"range":  "repos:\n  /r:\n    lanes: 2\n    deps: {2: [3]}\n",
		"self":   "repos:\n  /r:\n    deps: {2: [2]}\n",
		"lanes0": "defaults:\n  lanes: 100\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			writeConfig(t, body)
			_, err := Load("/r")
			if err == nil {
				t.Fatal("expected error")
			}
			if name == "cycle" && !strings.Contains(err.Error(), "cycle") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestEnsureRepoCreatesAndPreservesComments(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p := Path()

	created, added, err := EnsureRepo(p, "/repo/one")
	if err != nil || !created || !added {
		t.Fatalf("first EnsureRepo: created=%v added=%v err=%v", created, added, err)
	}
	// Second repo: comments from the default file must survive.
	if _, added, err = EnsureRepo(p, "/repo/two"); err != nil || !added {
		t.Fatalf("second EnsureRepo: added=%v err=%v", added, err)
	}
	// Idempotent.
	if _, added, err = EnsureRepo(p, "/repo/two"); err != nil || added {
		t.Fatalf("third EnsureRepo: added=%v err=%v", added, err)
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	for _, want := range []string{"# swim configuration", "/repo/one:", "/repo/two:", "header_env"} {
		if !strings.Contains(s, want) {
			t.Errorf("config missing %q:\n%s", want, s)
		}
	}
	c, err := Load("/repo/two")
	if err != nil || !c.HasRepo || c.Lanes != 4 {
		t.Fatalf("Load after EnsureRepo: %+v err=%v", c, err)
	}
}
