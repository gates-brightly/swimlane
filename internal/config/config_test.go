package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if c.Lanes != 1 || len(c.Active) != 0 || c.HasRepo {
		t.Fatalf("got lanes=%d active=%v hasRepo=%v", c.Lanes, c.Active, c.HasRepo)
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
	if a.Runtime != "node --version" || strings.Join(a.HeaderEnv, ",") != "STAGE" {
		t.Fatalf("a = %+v", a.Settings)
	}
	if got := a.DepsOf(4); len(got) != 1 || got[0] != 2 {
		t.Fatalf("DepsOf(4) = %v", got)
	}
	b, err := Load("/repo/b")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(b.HeaderEnv, ",") != "AWS_PROFILE" || len(b.Deps) != 0 {
		t.Fatalf("b = %+v", b.Settings)
	}
}

func TestValidateRejectsCyclesAndRange(t *testing.T) {
	cases := map[string]string{
		"cycle": "repos:\n  /r:\n    deps: {1: [2], 2: [3], 3: [1]}\n",
		"range": "repos:\n  /r:\n    deps: {2: [100]}\n",
		"self":  "repos:\n  /r:\n    deps: {2: [2]}\n",
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
	if err != nil || !c.HasRepo {
		t.Fatalf("Load after EnsureRepo: %+v err=%v", c, err)
	}
}

// chime was removed: a swim.yml (read strictly) or config.yml that still
// sets its keys loads, and RemovedKeys names them for swim doctor.
func TestRemovedChimeKeysStillLoad(t *testing.T) {
	writeConfig(t, "defaults:\n  chime: true\n  chime_min_s: 30\n")
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, RepoFileName), []byte("chime: failure\nchime_style: sound\n"), 0o644)
	c, err := Load(root)
	if err != nil {
		t.Fatalf("a config with chime keys: %v", err)
	}
	if got := strings.Join(c.Repo.RemovedKeys(), ","); got != "chime,chime_style" {
		t.Errorf("swim.yml removed keys: %q", got)
	}
	if got := strings.Join(c.Settings.RemovedKeys(), ","); got != "chime,chime_min_s" {
		t.Errorf("config.yml removed keys: %q", got)
	}
	if _, err := c.Get("chime"); err == nil {
		t.Error("swim config chime still answers")
	}
}

func TestSetKeyRoundTripKeepsComments(t *testing.T) {
	p := writeConfig(t, `# top comment
defaults:
  lanes: 4
  # interrupt comment
  interrupt: graceful # trailing
repos:
  /repo/a:
    # keep me
    deps: {2: [1]}
`)
	if err := SetKey(p, "/repo/a", "interrupt", "immediate", false); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(p, "/repo/a", "approvals", "flags", false); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(p, "/repo/a", "watch_batches", "2", true); err != nil {
		t.Fatal(err)
	}
	c, err := Load("/repo/a")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"interrupt": "immediate", "approvals": "flags", "watch_batches": "2"} {
		if got, _ := c.Get(key); got != want {
			t.Errorf("Get(%s) = %q, want %q", key, got, want)
		}
	}
	if o, _ := Load("/other"); o.WatchBatchesFor() != DefaultWatchBatches {
		t.Errorf("--repo write leaked to defaults: %d", o.WatchBatchesFor())
	}
	data, _ := os.ReadFile(p)
	s := string(data)
	for _, want := range []string{"# top comment", "# interrupt comment", "# trailing", "# keep me", "interrupt: immediate", "watch_batches: 2"} {
		if !strings.Contains(s, want) {
			t.Errorf("lost %q:\n%s", want, s)
		}
	}
	// A value is replaced in place, its comment kept.
	if err := SetKey(p, "/repo/a", "interrupt", "graceful", false); err != nil {
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
	if err := SetKey(p, "/repo/x", "interrupt", "immediate", false); err != nil {
		t.Fatal(err)
	}
	c, err := Load("/repo/x")
	if err != nil || c.InterruptMode() != InterruptImmediate || c.HasRepo {
		t.Fatalf("new file: %+v %v", c, err)
	}
	// An empty file and a flow-style `defaults: {}` both work.
	writeConfig(t, "")
	if err := SetKey(Path(), "/r", "approvals", "flags", false); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, "defaults: {}\n")
	if err := SetKey(Path(), "/r", "watch_batches", "5", false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(Path())
	if !strings.Contains(string(data), "defaults:\n  watch_batches: 5") {
		t.Errorf("flow defaults:\n%s", data)
	}
}

func TestSetKeyRejectsBadKeysAndValues(t *testing.T) {
	p := writeConfig(t, "defaults:\n  lanes: 4\n")
	before, _ := os.ReadFile(p)
	cases := []struct{ key, value, want string }{
		{"colour", "red", "keys: interrupt, interrupt_grace"},
		{"interrupt", "maybe", "graceful or immediate"},
		{"approvals", "sometimes", "prompt or flags"},
		{"watch_batches", "0", "1 or more"},
		{"watch_batches", "ten", "1 or more"},
		{"chime", "true", "unknown config key"}, // removed
		{"lanes", "4", "unknown config key"},    // swim finds the lanes in use
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

func TestRepoFile(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "swim.yml"), []byte("lanes: 7\nmax_parallel: 3\nblocked_commands: [terraform destroy]\n"), 0o644)
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	// lanes: is read only to say it's ignored (swim doctor).
	if c.Lanes != 1 || c.Parallel() != 3 || !c.RepoLanes || c.RepoFile == "" {
		t.Fatalf("swim.yml not applied: %+v", c)
	}
	// The operator's repos entry wins over swim.yml.
	os.MkdirAll(filepath.Join(xdg, "swim"), 0o755)
	os.WriteFile(filepath.Join(xdg, "swim", "config.yml"), []byte("defaults:\n  lanes: 4\nrepos:\n  "+root+":\n    max_parallel: 5\n"), 0o644)
	if c, _ = Load(root); c.Parallel() != 5 {
		t.Fatalf("precedence: parallel %d", c.Parallel())
	}
	os.WriteFile(filepath.Join(root, "swim.yml"), []byte("lanse: 3\n"), 0o644)
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "swim.yml") {
		t.Fatalf("unknown key should be an error naming swim.yml: %v", err)
	}
}

// Runners merge per extension: config defaults, then the repo's swim.yml,
// then the operator's repo section; env: is swim.yml's alone.
func TestRunnersAndEnv(t *testing.T) {
	xdg, root := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	os.MkdirAll(filepath.Join(xdg, "swim"), 0o755)
	os.WriteFile(filepath.Join(xdg, "swim", "config.yml"), []byte("defaults:\n  runners: {ts: deno, py: python3}\nrepos:\n  "+root+":\n    runners: {ts: tsx}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "swim.yml"), []byte("runners: {ts: bun, rb: ruby}\nenv: {STAGE: dev}\n"), 0o644)
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Runners["ts"] != "tsx" || c.Runners["py"] != "python3" || c.Runners["rb"] != "ruby" || filepath.Base(c.RunnerFrom["rb"]) != "swim.yml" {
		t.Errorf("runners %v from %v", c.Runners, c.RunnerFrom)
	}
	if c.Env["STAGE"] != "dev" {
		t.Errorf("env %v", c.Env)
	}
	if v, _ := c.Get("runners.ts"); v != "tsx" {
		t.Errorf("Get runners.ts = %q", v)
	}
	os.WriteFile(filepath.Join(xdg, "swim", "config.yml"), []byte("defaults:\n  env: {A: b}\n"), 0o644)
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "env: belongs in") {
		t.Errorf("env in config.yml: %v", err)
	}
}

func TestNewKeys(t *testing.T) {
	for key, val := range map[string]string{"runners.ts": "tsx --quiet", "msg_log_interval": "30s", "outputs_keep": "14d"} {
		if _, err := Normalize(key, val); err != nil {
			t.Errorf("%s %s: %v", key, val, err)
		}
	}
	for key, val := range map[string]string{"runners.ts": "", "msg_log_interval": "soon", "outputs_keep": "forever", "runners.": "x"} {
		if _, err := Normalize(key, val); err == nil {
			t.Errorf("%s %q accepted", key, val)
		}
	}
	for s, want := range map[string]time.Duration{"14d": 14 * 24 * time.Hour, "1d12h": 36 * time.Hour, "36h": 36 * time.Hour} {
		if got, ok := parseDays(s); !ok || got != want {
			t.Errorf("parseDays(%s) = %v %v", s, got, ok)
		}
	}
	c := &Config{}
	if c.MsgLogEvery() != 10*time.Second || c.OutputsKeepFor() != 14*24*time.Hour {
		t.Error("defaults")
	}
}

// secrets: (guarded secrets) and ambient_credentials: are swim.yml's alone,
// and change the policy hash.
func TestGuardedSecretsConfig(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	root := t.TempDir()
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	plain := c.PolicyHash()
	os.WriteFile(filepath.Join(root, "swim.yml"), []byte("secrets:\n  PROD_DB: { guard: ALLOW_DROP }\n  ROLE: { guard: [A, B], release: step }\nambient_credentials: [prod-write]\n"), 0o644)
	if c, err = Load(root); err != nil {
		t.Fatal(err)
	}
	if g := c.SecretGuard("PROD_DB"); len(g) != 1 || g[0] != "ALLOW_DROP" {
		t.Errorf("PROD_DB: %v", g)
	}
	if p := c.GuardedSecrets()["ROLE"]; strings.Join(p.Guard, ",") != "A,B" || p.Release != ReleaseStep {
		t.Errorf("ROLE: %+v", p)
	}
	if len(c.SecretGuard("other")) != 0 || strings.Join(c.AmbientCredentials, ",") != "prod-write" {
		t.Errorf("other / ambient: %v %v", c.SecretGuard("other"), c.AmbientCredentials)
	}
	if c.PolicyHash() == plain {
		t.Error("a guard didn't change the policy hash")
	}
	for body, want := range map[string]string{
		"secrets:\n  X: { release: step }\n":              "needs a guard",
		"secrets:\n  X: { guard: 'not a flag' }\n":        "isn't a guard flag",
		"secrets:\n  X: { guard: F, release: forever }\n": "want lane or step",
		"secrets:\n  1x: { guard: F }\n":                  "isn't a secret name",
	} {
		os.WriteFile(filepath.Join(root, "swim.yml"), []byte(body), 0o644)
		if _, err := Load(root); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", body, err, want)
		}
	}
	os.WriteFile(filepath.Join(root, "swim.yml"), nil, 0o644)
	os.MkdirAll(filepath.Join(xdg, "swim"), 0o755)
	os.WriteFile(filepath.Join(xdg, "swim", "config.yml"), []byte("defaults:\n  secrets:\n    X: { guard: F }\n"), 0o644)
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "belong in a repo's committed swim.yml") {
		t.Errorf("secrets: in config.yml: %v", err)
	}
}
