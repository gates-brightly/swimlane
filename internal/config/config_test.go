package config

import (
	"fmt"
	"os"
	"os/exec"
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
	if c.Lanes != 1 || len(c.Active) != 0 || c.Repo != "repo" || c.From("term_grace") != LayerDefault {
		t.Fatalf("got lanes=%d active=%v repo=%q", c.Lanes, c.Active, c.Repo)
	}
}

// gitRepo makes a git repo on branch, with origin as its origin remote
// when it isn't "".
func gitRepo(t *testing.T, origin, branch string) string {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", branch}, {"remote", "add", "origin", origin}} {
		if args[0] == "remote" && origin == "" {
			continue
		}
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return root
}

func TestRepoName(t *testing.T) {
	for origin, want := range map[string]string{
		"git@github.com:gates-brightly/swimlane.git": "swimlane",
		"https://github.com/gates-brightly/web/":     "web",
		"ssh://git@host:22/team/api.git":             "api",
		"/srv/git/infra.git":                         "infra",
	} {
		if got := RepoName(gitRepo(t, origin, "main")); got != want {
			t.Errorf("RepoName(origin %s) = %q, want %q", origin, got, want)
		}
	}
	// No origin: the toplevel's directory name.
	root := gitRepo(t, "", "main")
	if got := RepoName(root); got != filepath.Base(root) {
		t.Errorf("RepoName without origin = %q, want %q", got, filepath.Base(root))
	}
	if got := RepoBranch(root); got != "main" {
		t.Errorf("RepoBranch = %q", got)
	}
	if got := RepoBranch(t.TempDir()); got != "" {
		t.Errorf("RepoBranch outside git = %q", got)
	}
}

// Layers apply in order, later winning key by key: defaults, the repo's
// section (by name), then the branch's.
func TestLoadLayers(t *testing.T) {
	root := gitRepo(t, "git@github.com:me/swimlane.git", "feature")
	writeConfig(t, `
defaults:
  lanes: 4
  header_env: [STAGE]
  runtime: node --version
  term_grace: 20s
  blocked_commands: [rm -rf]
repos:
  swimlane:
    deps: {2: [1]} # removed: config.yml ignores it
    term_grace: 30s
    interrupt: immediate
    blocked_commands: [terraform destroy]
    runners: {ts: tsx}
  swimlane/feature:
    interrupt: graceful
    runners: {py: python3}
  swimlane/main:
    signing: off
  `+root+`:
    max_parallel: 9 # a path key: matches nothing
`)
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Repo != "swimlane" || c.Branch != "feature" || len(c.Layers) != 3 || !c.Layers[1].Found || !c.Layers[2].Found {
		t.Fatalf("repo %q branch %q layers %+v", c.Repo, c.Branch, c.Layers)
	}
	_, tg := c.Graces()
	if c.Runtime != "node --version" || tg.String() != "30s" || c.InterruptMode() != InterruptGraceful || c.SigningMode() != SigningAuto || c.Parallel() != 0 {
		t.Fatalf("effective: %+v", c.Settings)
	}
	if strings.Join(c.BlockedCommands, ",") != "rm -rf,terraform destroy" || c.Runners["ts"] != "tsx" || c.Runners["py"] != "python3" {
		t.Errorf("lists and runners: %v %v", c.BlockedCommands, c.Runners)
	}
	for key, want := range map[string]string{"runtime": LayerDefaults, "term_grace": LayerRepo, "interrupt": LayerBranch,
		"signing": LayerDefault, "runners.ts": LayerRepo, "runners.py": LayerBranch, "watch_batches": LayerDefault} {
		if got := c.From(key); got != want {
			t.Errorf("From(%s) = %s, want %s", key, got, want)
		}
	}
	if v, _ := c.Upto(LayerDefaults).Get("term_grace"); v != "20s" {
		t.Errorf("Upto(defaults) term_grace = %s", v)
	}
	if v, _ := c.Below(LayerBranch).Get("interrupt"); v != InterruptImmediate {
		t.Errorf("Below(branch) interrupt = %s", v)
	}
	if s, _ := c.Section(LayerBranch); s != "swimlane/feature" {
		t.Errorf("Section(branch) = %q", s)
	}
	if keys, _ := PathKeys(Path()); len(keys) != 1 || keys[0] != root {
		t.Errorf("PathKeys = %v", keys)
	}
}

// A detached HEAD has no branch layer.
func TestDetachedHeadHasNoBranchLayer(t *testing.T) {
	root := gitRepo(t, "", "main")
	for _, args := range [][]string{{"commit", "-q", "--allow-empty", "-m", "x"}, {"checkout", "-q", "--detach"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	name := filepath.Base(root)
	writeConfig(t, "repos:\n  "+name+"/main:\n    interrupt: immediate\n")
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Branch != "" || len(c.Layers) != 2 || c.InterruptMode() != InterruptGraceful {
		t.Fatalf("detached: branch %q layers %d interrupt %s", c.Branch, len(c.Layers), c.InterruptMode())
	}
	if _, err := c.Section(LayerBranch); err == nil {
		t.Error("a branch section on a detached HEAD")
	}
}

func TestEnsureFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := Path()
	if created, err := EnsureFile(p); err != nil || !created {
		t.Fatalf("first EnsureFile: %v %v", created, err)
	}
	os.WriteFile(p, []byte("# mine\n"), 0o644)
	if created, err := EnsureFile(p); err != nil || created {
		t.Fatalf("second EnsureFile: %v %v", created, err)
	}
	if b, _ := os.ReadFile(p); string(b) != "# mine\n" {
		t.Errorf("an existing file was rewritten: %q", b)
	}
}

func TestSetKeyRoundTripKeepsComments(t *testing.T) {
	p := writeConfig(t, `# top comment
defaults:
  lanes: 4
  # interrupt comment
  interrupt: graceful # trailing
repos:
  a:
    # keep me
    header_env: [STAGE]
`)
	if err := SetKey(p, "", "interrupt", "immediate"); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(p, "", "signing", "off"); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(p, "a", "watch_batches", "2"); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(p, "a/main", "term_grace", "30s"); err != nil {
		t.Fatal(err)
	}
	c, err := Load("/repo/a")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"interrupt": "immediate", "signing": "off", "watch_batches": "2"} {
		if got, _ := c.Get(key); got != want {
			t.Errorf("Get(%s) = %q, want %q", key, got, want)
		}
	}
	if o, _ := Load("/other"); o.WatchBatchesFor() != DefaultWatchBatches {
		t.Errorf("--repo write leaked to defaults: %d", o.WatchBatchesFor())
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	for _, want := range []string{"# top comment", "# interrupt comment", "# trailing", "# keep me", "interrupt: immediate", "watch_batches: 2", "a/main:\n    term_grace: 30s"} {
		if !strings.Contains(s, want) {
			t.Errorf("lost %q:\n%s", want, s)
		}
	}
	// A value is replaced in place, its comment kept.
	if err := SetKey(p, "", "interrupt", "graceful"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(p)
	if !strings.Contains(string(data), "interrupt: graceful # trailing") {
		t.Errorf("interrupt not written in place:\n%s", data)
	}
}

func TestSetKeyCreatesFileAndDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	p := Path()
	if err := SetKey(p, "", "interrupt", "immediate"); err != nil {
		t.Fatal(err)
	}
	c, err := Load("/repo/x")
	if err != nil || c.InterruptMode() != InterruptImmediate || c.Layers[1].Found {
		t.Fatalf("new file: %+v %v", c, err)
	}
	// An empty file and a flow-style `defaults: {}` both work, and a
	// repo's section is added to a file with no repos:.
	writeConfig(t, "")
	if err := SetKey(Path(), "", "signing", "off"); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, "defaults: {}\n")
	if err := SetKey(Path(), "", "watch_batches", "5"); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(Path(), "x", "watch_batches", "6"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(Path())
	if !strings.Contains(string(data), "defaults:\n  watch_batches: 5") || !strings.Contains(string(data), "repos:\n  x:\n    watch_batches: 6") {
		t.Errorf("flow defaults:\n%s", data)
	}
}

func TestSetKeyRejectsBadKeysAndValues(t *testing.T) {
	p := writeConfig(t, "defaults:\n  lanes: 4\n")
	before, _ := os.ReadFile(p)
	cases := []struct{ key, value, want string }{
		{"colour", "red", "keys: interrupt, interrupt_grace"},
		{"interrupt", "maybe", "graceful or immediate"},
		{"approvals", "flags", "unknown config key"}, // removed: --no-prompt, per run
		{"watch_batches", "0", "1 or more"},
		{"watch_batches", "ten", "1 or more"},
		{"chime", "true", "unknown config key"}, // removed
		{"lanes", "4", "unknown config key"},    // swim finds the lanes in use
	}
	for _, c := range cases {
		err := SetKey(p, "r", c.key, c.value)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("SetKey(%s, %s) err = %v, want %q", c.key, c.value, err, c.want)
		}
	}
	if after, _ := os.ReadFile(p); string(after) != string(before) {
		t.Errorf("rejected writes changed the file:\n%s", after)
	}
}

// A swim.yml in a repo is just a file: swim reads its config from
// config.yml alone.
func TestSwimYmlIsIgnored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "swim.yml"), []byte("max_parallel: 3\nlanse: 3\nenv: {A: b}\n"), 0o644)
	c, err := Load(root)
	if err != nil || c.Parallel() != 0 {
		t.Fatalf("swim.yml applied: %+v %v", c, err)
	}
}

// Runners merge per extension, defaults then the repo's section; env: in
// config.yml is not a key (env files hold env).
func TestRunners(t *testing.T) {
	writeConfig(t, "defaults:\n  runners: {ts: deno, py: python3}\n  env: {A: b}\nrepos:\n  r:\n    runners: {ts: tsx}\n")
	c, err := Load("/x/r")
	if err != nil {
		t.Fatal(err)
	}
	if c.Runners["ts"] != "tsx" || c.Runners["py"] != "python3" {
		t.Errorf("runners %v", c.Runners)
	}
	if v, _ := c.Get("runners.ts"); v != "tsx" {
		t.Errorf("Get runners.ts = %q", v)
	}
}

func TestNewKeys(t *testing.T) {
	for key, val := range map[string]string{"runners.ts": "tsx --quiet"} {
		if _, err := Normalize(key, val); err != nil {
			t.Errorf("%s %s: %v", key, val, err)
		}
	}
	for key, val := range map[string]string{"runners.ts": "", "runners.": "x"} {
		if _, err := Normalize(key, val); err == nil {
			t.Errorf("%s %q accepted", key, val)
		}
	}
}

func TestInherited(t *testing.T) {
	writeConfig(t, "defaults:\n  term_grace: 20s\n")
	c, err := Load("/x/r")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		layer, key, value string
		want              bool
	}{
		{LayerDefaults, "term_grace", "10s", true},
		{LayerDefaults, "term_grace", "20s", false},
		{LayerRepo, "term_grace", "20s", true},
		{LayerRepo, "term_grace", "10s", false},
		{LayerDefaults, "notify", "false", true},
		{LayerDefaults, "notify", "true", false},
		{LayerDefaults, "interrupt", "Graceful", true},
		{LayerDefaults, "watch_batches", "4", true},
	} {
		got, err := c.Inherited(tc.layer, tc.key, tc.value)
		if err != nil || got != tc.want {
			t.Errorf("Inherited(%s, %s, %s) = %v, %v; want %v", tc.layer, tc.key, tc.value, got, err, tc.want)
		}
	}
	if _, err := c.Inherited(LayerDefaults, "term_grace", "soon"); err == nil {
		t.Error("a bad value passed")
	}
}

func TestUnsetKeyKeepsTheRest(t *testing.T) {
	p := writeConfig(t, "defaults:\n  # first Ctrl-C\n  interrupt: immediate\n  notify: true # bar\nrepos:\n  r:\n    notify: false\n    interrupt: graceful\n")
	if err := UnsetKey(p, "", "interrupt"); err != nil {
		t.Fatal(err)
	}
	if err := UnsetKey(p, "r", "notify"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if s := string(b); strings.Contains(s, "interrupt: immediate") || !strings.Contains(s, "notify: true # bar") || strings.Contains(s, "notify: false") || !strings.Contains(s, "interrupt: graceful") {
		t.Errorf("after UnsetKey:\n%s", s)
	}
	if err := UnsetKey(p, "nope", "notify"); err != nil {
		t.Errorf("a missing section: %v", err)
	}
	if err := UnsetKey(filepath.Join(t.TempDir(), "none.yml"), "", "notify"); err != nil {
		t.Errorf("a missing file: %v", err)
	}
}

func TestBackupNumbersCopies(t *testing.T) {
	p := writeConfig(t, "defaults:\n  notify: true\n")
	if bak, err := Backup(p); err != nil || bak != p+"-1.bak" {
		t.Fatalf("first backup = %q, %v", bak, err)
	}
	if bak, err := Backup(p); err != nil || bak != "" {
		t.Errorf("an unchanged file was copied again: %q, %v", bak, err)
	}
	os.WriteFile(p, []byte("defaults:\n  notify: false\n"), 0o644)
	os.WriteFile(p+"-7.bak", []byte("older"), 0o644)
	os.WriteFile(p+"-x.bak", []byte("not a copy"), 0o644)
	if bak, err := Backup(p); err != nil || bak != p+"-8.bak" {
		t.Fatalf("after -7: %q, %v", bak, err)
	}
	if b, _ := os.ReadFile(p + "-8.bak"); string(b) != "defaults:\n  notify: false\n" {
		t.Errorf("copy holds %q", b)
	}
	if bak, err := Backup(filepath.Join(t.TempDir(), "none.yml")); err != nil || bak != "" {
		t.Errorf("a missing file: %q, %v", bak, err)
	}
}

// config.yml is read loosely: UnknownKeys names what swim doesn't read,
// with its section and line.
func TestUnknownKeys(t *testing.T) {
	p := writeConfig(t, `defaults:
  lanes: 4
  notify_ulr: https://ntfy.sh/x
  runners: {ts: tsx}
repos:
  shop:
    max_parallel: 2
    deps: {2: [1]}
  shop/main:
    modules: {allow: [notify]}
    term_grace: 30s
hub: true
`)
	got, err := UnknownKeys(p)
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, k := range got {
		s = append(s, fmt.Sprintf("%s@%s:%d", k.Key, k.Section, k.Line))
	}
	if strings.Join(s, " ") != "lanes@defaults:2 notify_ulr@defaults:3 deps@repos shop:8 modules@repos shop/main:10 hub@:12" {
		t.Errorf("unknown keys: %v", s)
	}
	if got, err := UnknownKeys(filepath.Join(t.TempDir(), "none.yml")); err != nil || len(got) != 0 {
		t.Errorf("missing file: %v %v", got, err)
	}
}
