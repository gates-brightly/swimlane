package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envFiles writes the operator's env.swim (general keys and this repo's
// section), the repo's .env.swim (lane sections) and swim.yml's env:.
func envFiles(t *testing.T, r *repo, user, laneFile, yml string) {
	t.Helper()
	dir := filepath.Join(envOf(r, "XDG_CONFIG_HOME"), "swim")
	os.MkdirAll(dir, 0o755)
	if user != "" {
		os.WriteFile(filepath.Join(dir, "env.swim"), []byte(strings.ReplaceAll(user, "REPO", r.root)), 0o644)
	}
	if laneFile != "" {
		r.write(".env.swim", laneFile, 0o600)
	}
	if yml != "" {
		r.write("swim.yml", yml, 0o644)
	}
}

// Each layer's keys reach the lane, the shell beats every file, values are
// masked in the log, and the round records which files applied and their
// hash (status and the chain).
func TestEnvFiles(t *testing.T) {
	r := newRepo(t, "")
	envFiles(t, r,
		"STAGE=general-stage\nREGION=general-region\nGENERAL_ONLY=general-value\n[repo REPO]\nPROFILE=orders-prod-profile\nREGION=repo-region\n",
		"[lane 1]\nTABLE=orders-v2-table\nSTAGE=lane-stage\n[lane 2]\nTABLE=other-lane-table\n",
		"env:\n  REGION: yml-region\n  OWNER: platform-team\n")
	r.script(1, "env", `run "env" bash -c 'echo "$STAGE $REGION $PROFILE $TABLE $OWNER $GENERAL_ONLY $SHELL_ONLY" > "$SWIM_ROOT/env.out"'`)
	r.env = append(r.env, "SHELL_ONLY=typed", "OWNER=shell-owner")
	r.mustSwim("run", "1", "--plain")
	data, _ := os.ReadFile(filepath.Join(r.root, "env.out"))
	if got, want := strings.TrimSpace(string(data)), "lane-stage repo-region orders-prod-profile orders-v2-table shell-owner general-value typed"; got != want {
		t.Errorf("lane env:\n got %s\nwant %s", got, want)
	}
	log := r.log(1)
	contains(t, "log", log, "env-files: ", "swim.yml[env]", "[repo]", ".env.swim[lane 1]")
	for _, secret := range []string{"orders-v2-table", "orders-prod-profile"} {
		if strings.Contains(log, secret) {
			t.Errorf("file value %q in the log", secret)
		}
	}
	sum := r.status().Get(1).EnvSHA
	if len(sum) != 64 {
		t.Fatalf("status env_sha256 = %q", sum)
	}
	chain, _ := os.ReadFile(filepath.Join(r.root, ".swim", "chain.jsonl"))
	contains(t, "chain", string(chain), `"env_sha256":"`+sum+`"`)

	out := r.mustSwim("env", "1")
	contains(t, "swim env", out, "TABLE", "lane 1", ".env.swim:2", "PROFILE", "repo", "OWNER", "(the shell's value wins)")
	if strings.Contains(out, "orders-v2-table") {
		t.Errorf("swim env shows a value unmasked:\n%s", out)
	}
	contains(t, "swim env --yaml", r.mustSwim("env", "1", "--yaml"), "schema: swim.env/v1", "env_sha256: "+sum, "key: TABLE", "scope: lane 1")

	// env: belongs in env.swim, not config.yml.
	cfg := filepath.Join(envOf(r, "XDG_CONFIG_HOME"), "swim", "config.yml")
	orig, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, []byte(strings.Replace(string(orig), "defaults:\n", "defaults:\n  env: {A: b}\n", 1)), 0o644)
	if out, code := r.swim("status"); code == 0 || !strings.Contains(out, "env: belongs in") {
		t.Errorf("env in config.yml: exit %d\n%s", code, out)
	}
	os.WriteFile(cfg, orig, 0o644)
}

// Approvals never come from a file: a guard flag in any env file (or a key
// matching a lookup.yml guard glob) stops the run before anything starts.
func TestEnvFileGuardFlagRefused(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "guarded", `if guard ALLOW_DROP "drop t"; then run "dropped" true; fi`)
	r.script(2, "other", `run "ok" true`)
	envFiles(t, r, "", "[lane 1]\nALLOW_DROP=1\n", "")
	out, code := r.swim("run", "--plain")
	if code == 0 || !strings.Contains(out, ".env.swim:2 sets ALLOW_DROP, a guard flag of this lane's script: env files can't approve guards") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if r.log(1) != "" || r.log(2) != "" {
		t.Error("a lane ran")
	}
	contains(t, "lint", must(r.swim("lint")), "sets ALLOW_DROP", "[env]")
	contains(t, "doctor", must(r.swim("doctor")), "sets ALLOW_DROP", "[env-file]")

	// A lookup.yml guard glob covers flags a script doesn't name yet.
	envFiles(t, r, "FIN_ALLOW_ANYTHING=1\n", "[lane 1]\nTABLE=t\n", "")
	r.write("lookup.yml", "users: {}\nrequire:\n  - guards: [FIN_ALLOW_*]\n", 0o644)
	if out, code := r.swim("run", "2", "--plain"); code == 0 || !strings.Contains(out, `matches guards "FIN_ALLOW_*" in lookup.yml`) {
		t.Errorf("lookup glob: exit %d\n%s", code, out)
	}
	// A bad line names the file and line.
	envFiles(t, r, "STAGE=$HOME\n", "", "")
	if out, code := r.swim("run", "2", "--plain"); code == 0 || !strings.Contains(out, "env.swim:1: STAGE: no expansion") {
		t.Errorf("bad line: exit %d\n%s", code, out)
	}
}

// A queued lane runs with the env it was queued with: an edit since skips
// it, like an edited script.
func TestEnvFileQueuePin(t *testing.T) {
	r := newRepo(t, "")
	r.script(1, "pinned", `run "ok" true`)
	envFiles(t, r, "", "[lane 1]\nTABLE=one\n", "")
	r.mustSwim("queue", "add", "1")
	r.write(".env.swim", "[lane 1]\nTABLE=two\n", 0o600)
	out, _ := r.cmd(bin, "watch", "--once").CombinedOutput()
	contains(t, "watch", string(out), "env files changed since it was queued")
	if strings.Contains(r.log(1), "PASS  ok") {
		t.Error("the lane ran with a changed env")
	}
}

// Lint warns about a key set twice in one env file section; a lane in
// another language gets the env files too, masked in its log.
func TestEnvFileDuplicatesAndWrappedLane(t *testing.T) {
	r := newRepo(t, "")
	envFiles(t, r, "", "[lane 1]\nTABLE=first-table\nTABLE=orders-v2-table\n", "")
	r.write("lane.1.sh", "#!/usr/bin/env bash\n# Round: wrapped env\nswim run \"show\" -- bash -c 'echo \"table=$TABLE\"; echo \"$TABLE\" > \"$SWIM_ROOT/table.txt\"'\n", 0o755)
	contains(t, "lint", must(r.swim("lint", "1")), ".env.swim:3 sets TABLE again in [lane 1] (first at line 2; the last wins)")
	r.mustSwim("run", "1", "--plain")
	if got := readFile(t, r, "table.txt"); got != "orders-v2-table" {
		t.Errorf("the lane got TABLE=%q (the last value should win)", got)
	}
	if log := r.log(1); !strings.Contains(log, "table=***") || strings.Contains(log, "orders-v2-table") {
		t.Errorf("file value not masked in a wrapped lane's log:\n%s", log)
	}
}
