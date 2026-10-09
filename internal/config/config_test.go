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

func TestSetLanesKeepsCommentsAndOtherRepos(t *testing.T) {
	p := writeConfig(t, `# top comment
defaults:
  lanes: 4
repos:
  /other:
    lanes: 2
  /repo/a:
    # keep me
    deps: {2: [1]}
`)
	if err := SetLanes(p, "/repo/a", 6); err != nil {
		t.Fatal(err)
	}
	c, err := Load("/repo/a")
	if err != nil || c.Lanes != 6 || len(c.DepsOf(2)) != 1 {
		t.Fatalf("after SetLanes: %+v %v", c, err)
	}
	if o, _ := Load("/other"); o.Lanes != 2 {
		t.Errorf("other repo changed: %d", o.Lanes)
	}
	data, _ := os.ReadFile(p)
	for _, want := range []string{"# top comment", "# keep me"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("lost %q:\n%s", want, data)
		}
	}
	// A repo without a section gets one.
	if err := SetLanes(p, "/repo/new", 5); err != nil {
		t.Fatal(err)
	}
	if c, _ := Load("/repo/new"); c.Lanes != 5 || !c.HasRepo {
		t.Errorf("new repo: %+v", c)
	}
	if err := SetLanes(p, "/repo/a", 100); err == nil {
		t.Error("lanes=100 accepted")
	}
}

func TestChimeYAMLAcceptsBoolsAndFailure(t *testing.T) {
	cases := map[string]Chime{"true": ChimeOn, "false": ChimeOff, "failure": ChimeFailure, `"failure"`: ChimeFailure, "on": ChimeOn}
	for v, want := range cases {
		writeConfig(t, "defaults:\n  chime: "+v+"\n")
		c, err := Load("/r")
		if err != nil {
			t.Fatalf("chime: %s: %v", v, err)
		}
		if mode, _, _ := c.ChimeSettings(); mode != want {
			t.Errorf("chime: %s -> %q, want %q", v, mode, want)
		}
	}
}

func TestChimeDefaultsAndRepoOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load("/r")
	if err != nil {
		t.Fatal(err)
	}
	if mode, style, minS := c.ChimeSettings(); mode != ChimeOff || style != StyleBell || minS != 10 {
		t.Fatalf("defaults: %s %s %d", mode, style, minS)
	}
	writeConfig(t, `defaults:
  chime: true
  chime_style: sound
  chime_min_s: 30
repos:
  /r:
    chime: failure
    chime_min_s: 0
`)
	c, err = Load("/r")
	if err != nil {
		t.Fatal(err)
	}
	if mode, style, minS := c.ChimeSettings(); mode != ChimeFailure || style != StyleSound || minS != 0 {
		t.Fatalf("merged: %s %s %d", mode, style, minS)
	}
	if o, _ := Load("/other"); o == nil || *o.Chime != ChimeOn || *o.ChimeMinS != 30 {
		t.Fatalf("other repo: %+v", o)
	}
}

func TestChimeBadValuesRejected(t *testing.T) {
	cases := map[string]string{
		"chime word":   "defaults:\n  chime: sometimes\n",
		"chime number": "defaults:\n  chime: 3\n",
		"chime list":   "defaults:\n  chime: [true]\n",
		"style":        "defaults:\n  chime_style: kazoo\n",
		"repo style":   "repos:\n  /r:\n    chime_style: kazoo\n",
		"min negative": "defaults:\n  chime_min_s: -1\n",
		"min word":     "defaults:\n  chime_min_s: soon\n",
	}
	for name, body := range cases {
		writeConfig(t, body)
		if _, err := Load("/r"); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(name, "chime ") && !strings.Contains(err.Error(), "true, false or failure") {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

func TestSetKeyRoundTripKeepsComments(t *testing.T) {
	p := writeConfig(t, `# top comment
defaults:
  lanes: 4
  # chime comment
  chime: false # trailing
repos:
  /repo/a:
    # keep me
    deps: {2: [1]}
`)
	if err := SetKey(p, "/repo/a", "chime", "failure", false); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(p, "/repo/a", "chime_style", "notify", false); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(p, "/repo/a", "chime_min_s", "0", true); err != nil {
		t.Fatal(err)
	}
	c, err := Load("/repo/a")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"chime": "failure", "chime_style": "notify", "chime_min_s": "0", "lanes": "4"} {
		if got, _ := c.Get(key); got != want {
			t.Errorf("Get(%s) = %q, want %q", key, got, want)
		}
	}
	if o, _ := Load("/other"); *o.ChimeMinS != DefaultChimeMinS {
		t.Errorf("--repo write leaked to defaults: %d", *o.ChimeMinS)
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	for _, want := range []string{"# top comment", "# chime comment", "# trailing", "# keep me", "chime: failure", "chime_min_s: 0"} {
		if !strings.Contains(s, want) {
			t.Errorf("lost %q:\n%s", want, s)
		}
	}
	// true/false are written as YAML booleans and read back.
	if err := SetKey(p, "/repo/a", "chime", "true", false); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(p)
	if !strings.Contains(string(data), "chime: true # trailing") {
		t.Errorf("chime true not written in place:\n%s", data)
	}
	if c, _ := Load("/repo/a"); *c.Chime != ChimeOn {
		t.Errorf("chime = %q", *c.Chime)
	}
}

func TestSetKeyCreatesFileAndDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p := Path()
	if err := SetKey(p, "/repo/x", "chime", "true", false); err != nil {
		t.Fatal(err)
	}
	c, err := Load("/repo/x")
	if err != nil || *c.Chime != ChimeOn || c.HasRepo {
		t.Fatalf("new file: %+v %v", c, err)
	}
	// An empty file and a flow-style `defaults: {}` both work.
	writeConfig(t, "")
	if err := SetKey(Path(), "/r", "chime_style", "sound", false); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, "defaults: {}\n")
	if err := SetKey(Path(), "/r", "chime_min_s", "5", false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(Path())
	if !strings.Contains(string(data), "defaults:\n  chime_min_s: 5") {
		t.Errorf("flow defaults:\n%s", data)
	}
}

func TestSetKeyRejectsBadKeysAndValues(t *testing.T) {
	p := writeConfig(t, "defaults:\n  lanes: 4\n")
	before, _ := os.ReadFile(p)
	cases := []struct{ key, value, want string }{
		{"colour", "red", "keys: lanes, chime, chime_style, chime_min_s"},
		{"chime", "maybe", "true, false or failure"},
		{"chime_style", "kazoo", "bell, sound, notify"},
		{"chime_min_s", "-3", "0 or more"},
		{"chime_min_s", "ten", "0 or more"},
		{"lanes", "0", "1..99"},
		{"lanes", "many", "1..99"},
	}
	for _, c := range cases {
		err := SetKey(p, "/r", c.key, c.value, false)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("SetKey(%s, %s) err = %v, want %q", c.key, c.value, err, c.want)
		}
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Errorf("rejected writes changed the file:\n%s", after)
	}
}
